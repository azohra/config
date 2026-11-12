package config

import (
	"bytes"
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
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
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

// Bootstrap installs native resources before restoring Config snapshots, then
// applies defaults that can depend on restored applications. Native hooks run
// without Config's checkout lock, including hooks that invoke config restore.
func (m NativeMise) Bootstrap(restore func() error) error {
	if err := m.prepare(); err != nil {
		return err
	}
	if _, _, err := m.command("bootstrap", "--skip-dirty", "--skip", "macos-defaults,task,final-hook", "--yes"); err != nil {
		return fmt.Errorf("native setup before restoration: %w", err)
	}
	if err := restore(); err != nil {
		return err
	}
	if _, _, err := m.command("bootstrap", "--only", "macos-defaults,task,final-hook", "--yes"); err != nil {
		return fmt.Errorf("native setup after restoration: %w", err)
	}
	return nil
}

// run executes one planned step and reports what it changed. It runs outside
// Config's checkout lock: a native pre-defaults hook may call config restore,
// which takes that lock for its own retained resources. Planning the step
// again afterwards separates the items that changed from the ones left behind,
// whatever mise printed along the way.
func (m NativeMise) run(group UpdateGroup, scope UpdateScope) StepResult {
	step := nativeStepFor(group.ID)
	log := Logger{Out: m.Live.Stdout}
	result := StepResult{ID: group.ID, Name: group.Name, Status: StepDone, Command: "mise " + strings.Join(step.command(group), " ")}
	started := time.Now()
	log.Section(group.Name)
	var err error
	if group.ID == setupStepID {
		err = m.prepare()
	} else {
		err = m.check(context.Background())
	}
	captured := true
	if err == nil {
		result.Output, captured, err = m.command(step.command(group)...)
	}
	result.Duration = time.Since(started).Round(time.Millisecond)
	if err != nil {
		result.Status, result.Error = StepFailed, nativeText(err.Error())
		if !captured {
			result.Error += "; mise's output was shown on the terminal and is not recorded"
		}
		if strings.Contains(result.Error, nativeNeedsPassword) {
			// mise asks for the password itself when it runs on a terminal.
			result.Error += "; run `" + scope.Command() + " --yes` in a terminal"
		}
	}
	if len(group.Items) > 0 {
		remaining := map[string]bool{}
		fresh := step.plan(m, context.Background())
		for _, item := range fresh.Items {
			remaining[fresh.Key(item)] = true
		}
		for _, item := range group.Items {
			outcome := ItemResult{UpdateItem: item, Status: ItemUpdated}
			switch {
			case fresh.State == UpdateUnavailable:
				outcome.Status = ItemUnknown
			case remaining[group.Key(item)] && err != nil:
				outcome.Status = ItemFailed
			case remaining[group.Key(item)]:
				outcome.Status = ItemUnchanged
			}
			result.Items = append(result.Items, outcome)
			switch outcome.Status {
			case ItemUpdated:
				log.OK(item.Label())
			case ItemFailed:
				log.Error(item.Label() + ": not updated")
			default:
				log.Warn(item.Label() + ": " + string(outcome.Status))
			}
		}
	}
	if err != nil {
		log.Error(group.Name + ": " + result.Error)
	} else if len(group.Items) == 0 {
		log.OK(group.Name + " finished")
	}
	return result
}

// command runs one native step and returns the end of its output. A failure
// reports mise's own errors instead of an exit status. A terminal is passed to
// mise untouched, so it can still ask for a password there; that output is the
// person's to read, and captured is false.
func (m NativeMise) command(args ...string) (output string, captured bool, err error) {
	live := m.Live
	var capture nativeCapture
	if file, ok := live.Stderr.(*os.File); !ok || !term.IsTerminal(file.Fd()) {
		captured = true
		stderr := io.MultiWriter(live.Stderr, &capture)
		if live.Stdout == live.Stderr {
			live.Stdout = stderr
		} else {
			live.Stdout = io.MultiWriter(live.Stdout, &capture)
		}
		live.Stderr = stderr
	}
	err = live.Command("mise", args...)
	if err != nil && len(capture.errors) > 0 {
		err = errors.New(strings.Join(capture.errors, "; "))
	}
	return capture.tail(), captured, err
}

const nativeNeedsPassword = "an installer needs your administrator password, and mise had no terminal to ask for it"

// maxNativeOutput bounds the output kept for a step's record.
const maxNativeOutput = 64 << 10

// nativeCapture keeps the errors mise reports and the end of its output.
type nativeCapture struct {
	partial []byte
	output  []byte
	errors  []string
}

func (c *nativeCapture) Write(data []byte) (int, error) {
	c.output = append(c.output, data...)
	if len(c.output) > 2*maxNativeOutput {
		c.output = append(c.output[:0], c.output[len(c.output)-maxNativeOutput:]...)
	}
	c.partial = append(c.partial, data...)
	for {
		end := bytes.IndexByte(c.partial, '\n')
		if end < 0 {
			// Progress redrawn without a newline is not an error line.
			if len(c.partial) > maxNativeOutput {
				c.partial = c.partial[:0]
			}
			return len(data), nil
		}
		line := nativeText(string(c.partial[:end]))
		c.partial = c.partial[end+1:]
		// mise names the underlying cause on the line after its error.
		if cause, found := strings.CutPrefix(line, "caused by:"); found && len(c.errors) > 0 && !strings.Contains(c.errors[len(c.errors)-1], cause) {
			c.errors[len(c.errors)-1] += ":" + cause
			continue
		}
		message, found := strings.CutPrefix(line, "mise ERROR")
		message = strings.TrimSpace(message)
		if !found || message == "" || strings.HasPrefix(message, "Version:") || strings.HasPrefix(message, "Run with --verbose") {
			continue
		}
		if strings.Contains(message, "requires a password but no TTY is available") {
			message = nativeNeedsPassword
		}
		if !slices.Contains(c.errors, message) {
			c.errors = append(c.errors, message)
		}
	}
}

func (c *nativeCapture) tail() string {
	output := c.output
	if len(output) > maxNativeOutput {
		output = output[len(output)-maxNativeOutput:]
	}
	return nativeText(string(output))
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

// nativeWarnings keeps the warnings mise prints, the notes worth reading.
func nativeWarnings(output string) []string {
	var warnings []string
	for line := range strings.SplitSeq(output, "\n") {
		warning, ok := strings.CutPrefix(strings.TrimSpace(line), "mise WARN")
		warning = strings.TrimSpace(warning)
		if ok && !strings.Contains(warning, " already ") && !strings.HasPrefix(warning, "Use --force") {
			warnings = append(warnings, warning)
		}
	}
	return warnings
}

func nativeNote(line string) bool {
	if strings.HasPrefix(line, "mise WARN") || strings.HasPrefix(line, "mise scanning tracked configs") ||
		strings.HasPrefix(line, "mise bootstrap: ") {
		return true
	}
	// Per-part status, such as "mise repos: 36 repo(s) already current".
	if strings.HasPrefix(line, "mise ") && strings.Contains(line, " already ") {
		return true
	}
	if strings.Contains(line, ": skipped: ") || strings.Contains(line, " is kept: ") {
		return true
	}
	for _, suffix := range []string{
		": already up to date", ": nothing to prune", ": all formulae already poured",
		": all files are applied", "All tools are up to date", "all tools are installed",
		": already converged", "pruned configuration links [dryrun]",
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
