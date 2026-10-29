package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

const miseID = "mise"

// NativeMise presents mise's own state and plans. It does not maintain a
// second inventory or implement its package, dotfile, or repository managers.
type NativeMise struct {
	Paths   Paths
	Probe   Runner
	Live    LiveRunner
	Install func() error
}

func machineUsesMise(paths Paths) bool {
	_, err := os.Lstat(paths.InRoot("mise"))
	return !errors.Is(err, os.ErrNotExist)
}

func MachineUsesMise(paths Paths) bool { return machineUsesMise(paths) }

func NewNativeMise(paths Paths, out io.Writer) NativeMise {
	if out == nil {
		out = io.Discard
	}
	executable := paths.InHome(".local", "bin", "mise")
	unset := append(append([]string{}, gitLocalEnvironment...), miseLocalEnvironment...)
	unset = append(unset, "MISE_ENV", "MISE_CONFIG_FILE", "MISE_DEFAULT_CONFIG_FILENAME", "MISE_OVERRIDE_CONFIG_FILENAMES", "MISE_NO_ENV", "MISE_NO_HOOKS")
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "MISE_") && strings.HasSuffix(name, "_VERSION") {
			unset = append(unset, name)
		}
	}
	environment := []string{"MISE_CONFIG_DIR=" + miseConfigDir(paths), "MISE_GLOBAL_CONFIG_ROOT=" + paths.Home, "MISE_AUTO_UPDATE=0", "NO_COLOR=1"}
	probe := OSRunner{Dir: paths.Home, Unset: unset, Environment: environment, Executables: map[string]string{"mise": executable}}
	live := newLiveRunner(paths.Home)
	live.Unset, live.Environment, live.Executables = unset, environment, probe.Executables
	live.Stdout, live.Stderr = out, out
	return NativeMise{Paths: paths, Probe: probe, Live: live, Install: releaseMiseInstallerAt(executable).Install}
}

func (m NativeMise) check(ctx context.Context) error {
	if err := requireMiseConfigBinding(m.Paths); err != nil {
		return err
	}
	version, err := currentMiseVersionContext(ctx, m.Probe)
	if err != nil {
		return err
	}
	if compareConfigVersions("v"+version, "v"+releaseMiseVersion) < 0 {
		return fmt.Errorf("mise %s is too old; mise %s or newer is required", version, releaseMiseVersion)
	}
	return nil
}

func (m NativeMise) prepare() error {
	state, detail := inspectMiseConfigBinding(m.Paths)
	if state == miseConfigBindingInvalid || state == miseConfigBindingConflict {
		return errors.New(detail)
	}
	version, err := currentMiseVersionContext(context.Background(), m.Probe)
	if err != nil || compareConfigVersions("v"+version, "v"+releaseMiseVersion) < 0 {
		if m.Install == nil {
			return errors.New("mise installation is unavailable")
		}
		if err := m.Install(); err != nil {
			return err
		}
	}
	if err := ensureMiseConfigBinding(m.Paths); err != nil {
		return err
	}
	return m.check(context.Background())
}

func (m NativeMise) Inspect() Resource {
	resource := Resource{ID: miseID, Name: "Machine setup", State: Unavailable, Authoritative: true}
	if err := m.check(context.Background()); err != nil {
		resource.Summary = "native setup needs attention"
		resource.Checks = []Check{no("mise setup unavailable", nativeText(err.Error()))}
		return resource
	}
	result := run(m.Probe, "mise", "bootstrap", "status", "--json", "--missing")
	var status map[string]any
	if err := json.Unmarshal([]byte(result.Stdout), &status); err != nil || !nativeStatusReadable(status) {
		resource.Summary = "native status unavailable"
		detail := "mise returned an empty or unreadable status report"
		if result.Err != nil {
			detail = result.Failure().Error()
		}
		resource.Checks = []Check{no("machine state unreadable", nativeText(detail))}
		return resource
	}
	problems, checkouts := nativeStatusFindings(status, "")
	resource.Details = append(problems, checkouts...)
	switch {
	case result.Err != nil && (len(problems) > 0 || len(checkouts) == 0):
		resource.State, resource.Summary = Drift, "native resources need attention"
		resource.Checks = []Check{no("machine configuration differs", "Select Repair machine to review native setup; local repository edits are preserved")}
	case len(problems) > 0:
		resource.Summary = "some native resources could not be verified"
		resource.Checks = []Check{no("native inspection incomplete", "Review the provider details before treating the machine as current")}
	default:
		resource.State, resource.Summary = Current, "native resources match"
		if len(checkouts) > 0 {
			resource.Summary += "; " + FormatCount(len(checkouts), "repository checkout differs from its remote", "repository checkouts differ from their remotes")
		}
		resource.Checks = []Check{{Label: "native machine configuration", OK: true}}
	}
	return resource
}

func nativeStatusReadable(status map[string]any) bool {
	for _, key := range []string{"tools", "packages", "repos", "dotfiles", "macos_defaults", "macos_services", "macos_login_items"} {
		switch status[key].(type) {
		case []any, map[string]any:
			return true
		}
	}
	return false
}

// nativeStatusFindings lists the entries mise reports as unhealthy. A declared
// checkout that is clean but not at its remote ref, or that has local edits, is
// ordinary development state: mise also reports a remote it cannot reach as a
// difference. Those checkouts are listed apart so they show without failing
// machine health; a missing or foreign checkout is still a problem.
func nativeStatusFindings(value any, section string) (problems, checkouts []string) {
	switch value := value.(type) {
	case []any:
		for _, item := range value {
			p, c := nativeStatusFindings(item, section)
			problems, checkouts = append(problems, p...), append(checkouts, c...)
		}
	case map[string]any:
		state, hasState := value["state"].(string)
		if !hasState {
			state, hasState = value["action"].(string)
		}
		if hasState && !slices.Contains([]string{"installed", "applied", "tracked", "current", "set", "loaded", "noop", "present", "absent", "running", "ready", "available"}, state) {
			name := section
			for _, key := range []string{"package", "tool", "path", "target", "name", "label", "key"} {
				if text, ok := value[key].(string); ok && text != "" {
					name = text
					break
				}
			}
			line := nativeText(name + ": " + state)
			if section == "repos" && (state == "differs" || state == "dirty") {
				checkouts = append(checkouts, line)
			} else {
				problems = append(problems, line)
			}
		}
		if available, ok := value["available"].(bool); ok && !available {
			problems = append(problems, nativeText(section+": provider unavailable"))
		}
		for _, key := range slices.Sorted(maps.Keys(value)) {
			switch value[key].(type) {
			case []any, map[string]any:
				p, c := nativeStatusFindings(value[key], key)
				problems, checkouts = append(problems, p...), append(checkouts, c...)
			}
		}
	}
	return problems, checkouts
}

type nativeOperation struct {
	Name string
	Args []string
}

func nativeOperations(scope UpdateScope) []nativeOperation {
	switch scope {
	case UpdateSoftware:
		return []nativeOperation{
			{"Tools", []string{"upgrade", "--global", "--no-prune", "--yes"}},
			{"Apps and packages", []string{"bootstrap", "packages", "upgrade", "--yes"}},
		}
	case UpdateRepositories:
		return []nativeOperation{
			{"Repositories", []string{"bootstrap", "repos", "update", "--skip-dirty", "--yes"}},
			{"Skills, MCP and dotfiles", []string{"dotfiles", "apply", "--yes"}},
		}
	case UpdateRepair:
		return []nativeOperation{{"Machine setup", []string{"bootstrap", "--skip-dirty", "--yes"}}}
	}
	return nil
}

func (m NativeMise) plan(ctx context.Context, scope UpdateScope) []UpdateGroup {
	if err := m.check(ctx); err != nil {
		if scope == UpdateRepair {
			state, detail := inspectMiseConfigBinding(m.Paths)
			if state == miseConfigBindingInvalid || state == miseConfigBindingConflict {
				return []UpdateGroup{{Name: "Machine setup", State: UpdateUnavailable, Summary: nativeText(detail)}}
			}
			return []UpdateGroup{{Name: "Machine setup", State: UpdatePending, Summary: "Prepare verified mise and apply the declared machine setup", Details: []string{nativeText(err.Error()), "Repository edits are skipped; declared hooks and tasks will run"}}}
		}
		return []UpdateGroup{{Name: scope.Label(), State: UpdateUnavailable, Summary: nativeText(err.Error()), Details: []string{"Use Repair machine to prepare native mise setup"}}}
	}
	var groups []UpdateGroup
	for _, operation := range nativeOperations(scope) {
		args := append(slices.Clone(operation.Args), "--dry-run")
		result := runContext(ctx, m.Probe, "mise", args...)
		group := UpdateGroup{Name: operation.Name, State: UpdatePending, Summary: "Native plan; versions and pins are checked by mise"}
		preview, changes := nativePreview(result)
		if result.Err != nil {
			group.State, group.Summary = UpdateUnavailable, nativeText(result.Failure().Error())
		} else if changes == "" {
			group.State, group.Summary, group.Details = UpdateCurrent, "No changes planned", nativeWarnings(preview)
		} else {
			group.Details, group.Changes = strings.Split(preview, "\n"), changes
		}
		groups = append(groups, group)
	}
	return groups
}

// Bootstrap installs native resources before restoring Config snapshots, then
// applies defaults that can depend on restored applications. Native hooks run
// without Config's checkout lock, including hooks that invoke config restore.
func (m NativeMise) Bootstrap(restore func() error) error {
	if err := m.prepare(); err != nil {
		return err
	}
	if err := m.Live.Command("mise", "bootstrap", "--skip-dirty", "--skip", "macos-defaults,task,final-hook", "--yes"); err != nil {
		return fmt.Errorf("native setup before restoration: %w", err)
	}
	if err := restore(); err != nil {
		return err
	}
	if err := m.Live.Command("mise", "bootstrap", "--only", "macos-defaults,task,final-hook", "--yes"); err != nil {
		return fmt.Errorf("native setup after restoration: %w", err)
	}
	return nil
}

// Apply runs outside Config's checkout lock: a native pre-defaults hook may
// call config restore, which takes that lock for its own retained resources.
func (m NativeMise) Apply(scope UpdateScope) error {
	if scope == UpdateRepair {
		if err := m.prepare(); err != nil {
			return err
		}
	} else if err := m.check(context.Background()); err != nil {
		return err
	}
	var failures []error
	for _, operation := range nativeOperations(scope) {
		Logger{Out: m.Live.Stdout}.Section(operation.Name)
		if err := m.Live.Command("mise", operation.Args...); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", operation.Name, err))
			if scope == UpdateRepositories {
				break
			}
		}
	}
	return errors.Join(failures...)
}

func (m NativeMise) configurationDigest(ctx context.Context) (string, error) {
	listing := runContext(ctx, newGitRunner(m.Paths.Root), "git", "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if listing.Err != nil {
		return "", fmt.Errorf("read machine configuration: %w", listing.Failure())
	}
	paths := make(map[string]bool)
	for _, name := range strings.Split(listing.Stdout, "\x00") {
		if name == "" {
			continue
		}
		if !filepath.IsLocal(name) {
			return "", errors.New("machine inventory contains an unsafe path")
		}
		paths[m.Paths.InRoot(name)] = false
	}
	if m.check(ctx) == nil {
		// Native includes can live outside the managed checkout. Bind those
		// effective declarations as well as the machine's tracked source files.
		result := runContext(ctx, m.Probe, "mise", "config", "ls", "--json")
		var files []struct {
			Path string `json:"path"`
		}
		if result.Err != nil {
			return "", fmt.Errorf("read native configuration: %w", result.Failure())
		}
		if err := json.Unmarshal([]byte(result.Stdout), &files); err != nil {
			return "", fmt.Errorf("read native configuration: %w", err)
		}
		if files == nil {
			return "", errors.New("native configuration inventory is not an array")
		}
		for _, file := range files {
			if !filepath.IsAbs(file.Path) {
				return "", errors.New("native configuration inventory contains a relative path")
			}
			paths[file.Path] = true
		}
	}
	var content []byte
	for _, path := range slices.Sorted(maps.Keys(paths)) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			content = append(content, path+"\x00missing\x00"...)
			continue
		}
		if err != nil {
			return "", err
		}
		content = append(content, fmt.Sprintf("%s\x00%s\x00", path, info.Mode())...)
		if info.IsDir() {
			return "", fmt.Errorf("%s is not a configuration file", path)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return "", err
			}
			content = append(content, target...)
			// Bind authored links by target. Only effective configuration
			// files also need their destination's content in the plan.
			if !paths[path] {
				continue
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		content = append(content, data...)
		content = append(content, '\x00')
	}
	return contentDigest(content), nil
}

// nativePreview returns a native dry run for review and the lines in it that
// describe changes. Notes about items left alone, versions kept for running
// processes, progress and warnings change with the clock and the processes on
// the Mac, so a confirmation does not bind them and they are not work. A line
// this does not recognise counts as a change: reworded output makes a plan look
// pending instead of hiding work.
func nativePreview(result Result) (preview, changes string) {
	preview = nativeText(result.Stdout + "\n" + result.Stderr)
	var lines []string
	for _, line := range strings.Split(preview, "\n") {
		if line = strings.TrimSpace(line); line != "" && !nativeNote(line) {
			lines = append(lines, line)
		}
	}
	return preview, strings.Join(lines, "\n")
}

// nativeWarnings keeps what mise warned about in a preview that plans nothing.
func nativeWarnings(preview string) []string {
	var warnings []string
	for line := range strings.SplitSeq(preview, "\n") {
		if warning, ok := strings.CutPrefix(strings.TrimSpace(line), "mise WARN"); ok {
			warnings = append(warnings, strings.TrimSpace(warning))
		}
	}
	return warnings
}

func nativeNote(line string) bool {
	if strings.HasPrefix(line, "mise WARN") || strings.HasPrefix(line, "mise scanning tracked configs") {
		return true
	}
	if strings.Contains(line, ": skipped: ") || strings.Contains(line, " is kept: ") {
		return true
	}
	for _, suffix := range []string{
		": already up to date", ": nothing to prune", ": all formulae already poured",
		": all files are applied", "All tools are up to date", "pruned configuration links [dryrun]",
		"anyway, run again with --force",
	} {
		if strings.HasSuffix(line, suffix) {
			return true
		}
	}
	return false
}

func nativeText(value string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, ansi.Strip(value)))
}
