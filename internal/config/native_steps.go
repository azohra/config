package config

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// nativeStep is one native command of an update and the probe that lists what
// it would change. mise reports tools, packages, repositories and dotfiles as
// JSON, so a step's items come from that state and not from the wording of a
// dry run. Planning a step again after it ran shows what it left undone.
type nativeStep struct {
	id   string
	name string
	// args is the native command. A partial selection appends its targets.
	args []string
	plan func(NativeMise, context.Context) UpdateGroup
}

func nativeStepFor(id string) nativeStep {
	switch id {
	case miseStepID:
		return nativeStep{id, "mise", []string{"self-update", "--no-plugins", "--yes"}, NativeMise.planSelf}
	case toolsStepID:
		return nativeStep{id, "Tools", []string{"upgrade", "--global", "--no-prune", "--yes"}, NativeMise.planTools}
	case appsStepID:
		return nativeStep{id, "Apps and packages", []string{"bootstrap", "packages", "upgrade", "--yes"}, NativeMise.planApps}
	case repositoriesStepID:
		return nativeStep{id, "Repositories", []string{"bootstrap", "repos", "update", "--skip-dirty", "--yes"}, NativeMise.planRepositories}
	case filesStepID:
		return nativeStep{id, "Skills, MCP and dotfiles", []string{"dotfiles", "apply", "--yes"}, NativeMise.planFiles}
	case setupStepID:
		return nativeStep{id, "Machine setup", []string{"bootstrap", "--skip-dirty", "--yes"}, NativeMise.planSetup}
	}
	return nativeStep{}
}

// command is the native command for a planned step.
func (s nativeStep) command(group UpdateGroup) []string {
	args := slices.Clone(s.args)
	if group.Partial {
		for _, item := range group.Items {
			args = append(args, item.Target)
		}
	}
	return args
}

// plan probes each step of a scope. The probes are independent reads, so they
// run together.
func (m NativeMise) plan(ctx context.Context, scope UpdateScope) []UpdateGroup {
	ids := scope.steps()
	if err := m.check(ctx); err != nil {
		if scope == UpdateRepair {
			return []UpdateGroup{m.planSetup(ctx)}
		}
		return []UpdateGroup{{ID: ids[0], Name: scope.Label(), State: UpdateUnavailable, Summary: scope.Label() + ": " + nativeText(err.Error()), Details: []string{"Use Repair machine to prepare native mise setup"}}}
	}
	groups := make([]UpdateGroup, len(ids))
	var wait sync.WaitGroup
	for index, id := range ids {
		wait.Go(func() { groups[index] = nativeStepFor(id).plan(m, ctx) })
	}
	wait.Wait()
	return groups
}

func (m NativeMise) step(id string) UpdateGroup {
	step := nativeStepFor(id)
	return UpdateGroup{ID: step.id, Name: step.name, Command: "mise " + strings.Join(step.args, " ")}
}

func (g UpdateGroup) unavailable(err error) UpdateGroup {
	g.State, g.Summary, g.Items = UpdateUnavailable, g.Name+": "+nativeText(err.Error()), nil
	return g
}

// nativeJSON reads a probe's JSON report. An empty report is an empty state.
func nativeJSON(result Result, value any) error {
	if result.Err != nil {
		return result.Failure()
	}
	if strings.TrimSpace(result.Stdout) == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(result.Stdout), value); err != nil {
		return fmt.Errorf("mise returned an unreadable report: %w", err)
	}
	return nil
}

func (m NativeMise) planSelf(ctx context.Context) UpdateGroup {
	group := m.step(miseStepID)
	var report struct{ Version, Latest string }
	if err := nativeJSON(runContext(ctx, m.Probe, "mise", "version", "--json"), &report); err != nil {
		return group.unavailable(err)
	}
	current, ok := miseVersion(report.Version)
	if !ok {
		return group.unavailable(fmt.Errorf("mise version is unreadable"))
	}
	if compareConfigVersions("v"+report.Latest, "v"+current) > 0 {
		group.Items = []UpdateItem{{Name: group.Name, From: current, To: report.Latest, Target: miseStepID}}
	}
	group = group.itemized()
	if group.State == UpdateCurrent {
		group.Summary = "mise: " + current + " is current"
	} else {
		group.Summary = "mise: " + current + " → " + report.Latest
	}
	return group
}

type nativeOutdated struct {
	Current string  `json:"current"`
	Latest  string  `json:"latest"`
	Bump    *string `json:"bump"`
}

func (m NativeMise) planTools(ctx context.Context) UpdateGroup {
	group := m.step(toolsStepID)
	var outdated, newer map[string]nativeOutdated
	result := runContext(ctx, m.Probe, "mise", "outdated", "--global", "--json")
	if err := nativeJSON(result, &outdated); err != nil {
		return group.unavailable(err)
	}
	for _, name := range slices.Sorted(maps.Keys(outdated)) {
		if tool := outdated[name]; tool.Latest != "" && tool.Latest != tool.Current {
			group.Items = append(group.Items, UpdateItem{Name: nativeToolName(name), From: tool.Current, To: tool.Latest, Target: name})
		}
	}
	group.Details = nativeWarnings(nativeText(result.Stderr))
	// A pinned tool stays on its declared version. Newer releases are a note.
	pinned := runContext(ctx, m.Probe, "mise", "outdated", "--global", "--bump", "--json")
	if nativeJSON(pinned, &newer) == nil {
		var names []string
		for _, name := range slices.Sorted(maps.Keys(newer)) {
			if tool := newer[name]; tool.Bump != nil {
				names = append(names, nativeToolName(name)+" "+tool.Latest)
			}
		}
		if len(names) > 0 {
			group.Details = append([]string{"Newer than the declared versions, not installed: " + strings.Join(names, ", ")}, group.Details...)
		}
		for _, warning := range nativeWarnings(nativeText(pinned.Stderr)) {
			if !slices.Contains(group.Details, warning) {
				group.Details = append(group.Details, warning)
			}
		}
	}
	return group.itemized()
}

type nativePackages map[string]struct {
	Packages []struct {
		Package   string `json:"package"`
		Name      string `json:"name"`
		Installed string `json:"installed_version"`
	} `json:"packages"`
}

// installed finds a package by its name in a manager, with or without a tap.
func (p nativePackages) installed(manager, name string) (key, label, version string) {
	for _, entry := range p[manager].Packages {
		if entry.Package == name || strings.HasSuffix(entry.Package, "/"+name) {
			label = entry.Name
			if label == "" {
				label = name
			}
			return manager + ":" + entry.Package, label, entry.Installed
		}
	}
	return manager + ":" + name, name, ""
}

var (
	nativeHeldPackage = regexp.MustCompile(`^mise ([a-z-]+):(\S+): skipped: (.+)$`)
	nativePour        = regexp.MustCompile(`^(?:pour|build) ([^/\s]+)/(\S+) .*\((requested|dependency)`)
)

// planApps lists package upgrades. mise reports installed versions as JSON; the
// version an upgrade installs appears only in its dry run, one line an item.
// Some lines say nothing about whether there is an update: mise prints an App
// Store or plugin upgrade for every declared package, pours a formula's
// dependencies, and may print a line Config does not recognise. Those are
// listed as commands the whole step also runs, and keep the step runnable, so
// a plan neither counts them as updates nor hides them.
func (m NativeMise) planApps(ctx context.Context) UpdateGroup {
	group := m.step(appsStepID)
	var installed nativePackages
	if err := nativeJSON(runContext(ctx, m.Probe, "mise", "bootstrap", "packages", "status", "--json"), &installed); err != nil {
		return group.unavailable(err)
	}
	result := runContext(ctx, m.Probe, "mise", append(nativeStepFor(appsStepID).args, "--dry-run")...)
	if result.Err != nil {
		return group.unavailable(result.Failure())
	}
	preview, changes := nativePreview(result)
	group.Raw = strings.Split(preview, "\n")
	held := map[string][]string{}
	var reasons, also, store []string
	for line := range strings.SplitSeq(preview, "\n") {
		if match := nativeHeldPackage.FindStringSubmatch(strings.TrimSpace(line)); match != nil {
			if _, seen := held[match[3]]; !seen {
				reasons = append(reasons, match[3])
			}
			held[match[3]] = append(held[match[3]], match[2])
		}
	}
	for line := range strings.SplitSeq(changes, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "install pkg ") || strings.HasPrefix(line, "sudo ") ||
			strings.HasPrefix(line, "run installer ") && strings.HasSuffix(line, " with sudo"):
			group.Elevated = true
		case strings.HasPrefix(line, "install cask "):
			token, version, _ := strings.Cut(strings.TrimPrefix(line, "install cask "), "/")
			key, label, from := installed.installed("brew-cask", token)
			group.Items = append(group.Items, UpdateItem{Name: label, From: from, To: version, Target: key})
		case nativePour.MatchString(line):
			match := nativePour.FindStringSubmatch(line)
			if match[3] == "requested" {
				key, label, from := installed.installed("brew", match[1])
				group.Items = append(group.Items, UpdateItem{Name: label, From: from, To: match[2], Target: key})
			} else {
				also = append(also, "dependency "+match[1]+" "+match[2])
			}
		case strings.HasPrefix(line, "mas install ") || strings.HasPrefix(line, "mas upgrade "):
			for _, id := range strings.Fields(line)[2:] {
				_, label, _ := installed.installed("mas", id)
				store = append(store, label)
			}
		case strings.HasPrefix(line, "mise ["):
			_, command, _ := strings.Cut(strings.TrimPrefix(line, "mise ["), "] ")
			also = append(also, command)
		case nativeChangePart(line):
		default:
			also = append(also, line)
		}
	}
	if len(store) > 0 {
		also = append([]string{"App Store upgrade for " + strings.Join(store, ", ")}, also...)
	}
	if len(also) > 0 {
		group.Details = append(group.Details, "Also runs with the whole step, updates unknown until then: "+strings.Join(also, "; "))
	}
	for _, reason := range reasons {
		group.Details = append(group.Details, "Left alone, "+reason+": "+strings.Join(held[reason], ", "))
	}
	group.Details = append(group.Details, nativeWarnings(preview)...)
	group = group.itemized()
	if group.State == UpdateCurrent && len(also) > 0 {
		group.State, group.Summary = UpdatePending, group.Name+": no known updates; its other commands check when run"
		group.Changes = strings.Join(also, "\n")
	}
	return group
}

func (m NativeMise) planRepositories(ctx context.Context) UpdateGroup {
	group := m.step(repositoriesStepID)
	var report struct {
		Repos []struct {
			Path    string `json:"path_raw"`
			Ref     string `json:"ref"`
			Current string `json:"current_ref"`
			State   string `json:"state"`
			Reason  string `json:"reason"`
		} `json:"repos"`
	}
	if err := nativeJSON(runContext(ctx, m.Probe, "mise", "bootstrap", "repos", "status", "--json"), &report); err != nil {
		return group.unavailable(err)
	}
	var edited, unchecked []string
	for _, repo := range report.Repos {
		switch repo.State {
		case "current":
			// Without a declared ref mise has nothing to compare, and reports
			// the checkout current until an update fetches it.
			if repo.Ref == "" {
				unchecked = append(unchecked, repo.Path)
			}
		case "differs":
			item := UpdateItem{Name: repo.Path, Note: repo.Ref, Target: repo.Path}
			if repo.Current != "" && repo.Current != repo.Ref {
				item.From, item.To, item.Note = repo.Current, repo.Ref, ""
			}
			group.Items = append(group.Items, item)
		case "missing":
			group.Items = append(group.Items, UpdateItem{Name: repo.Path, Note: "clone", Target: repo.Path})
		case "dirty":
			edited = append(edited, repo.Path)
		default:
			group.Details = append(group.Details, nativeText(strings.TrimSpace(repo.Path+": "+repo.State+" "+repo.Reason)))
		}
	}
	if len(edited) > 0 {
		group.Details = append(group.Details, "Left alone, local edits: "+strings.Join(edited, ", "))
	}
	if len(group.Items) > 0 {
		group.Details = append(group.Details, "Fast-forward only; a checkout that is ahead of its remote or cannot reach it stays as it is")
	}
	if len(unchecked) > 0 {
		group.Details = append(group.Details, "Not checked, no ref declared; pulled when every repository updates: "+strings.Join(unchecked, ", "))
	}
	return group.itemized()
}

func (m NativeMise) planFiles(ctx context.Context) UpdateGroup {
	group := m.step(filesStepID)
	type entry struct {
		Target string `json:"target"`
		Path   string `json:"path"`
		Edit   string `json:"edit"`
		State  string `json:"state"`
	}
	var report struct{ Files, Edits []entry }
	if err := nativeJSON(runContext(ctx, m.Probe, "mise", "dotfiles", "status", "--json"), &report); err != nil {
		return group.unavailable(err)
	}
	for _, file := range append(report.Files, report.Edits...) {
		if file.State == "applied" {
			continue
		}
		target := file.Target
		if target == "" {
			target = file.Path
		}
		item := UpdateItem{Name: target, Note: file.State, Target: target}
		if file.Edit != "" {
			item.Name += " " + file.Edit
		}
		group.Items = append(group.Items, item)
	}
	return group.itemized()
}

// planSetup previews the whole native bootstrap. Its hooks, tasks and defaults
// have no state to read, so this step is reviewed from mise's dry run.
func (m NativeMise) planSetup(ctx context.Context) UpdateGroup {
	group := m.step(setupStepID)
	if err := m.check(ctx); err != nil {
		state, detail := inspectMiseConfigBinding(m.Paths)
		if state == miseConfigBindingInvalid || state == miseConfigBindingConflict {
			return group.unavailable(fmt.Errorf("%s", detail))
		}
		group.State, group.Summary = UpdatePending, "Machine setup: install verified mise, then apply the declared setup"
		group.Details = []string{nativeText(err.Error()), "Repository edits are skipped; declared hooks and tasks will run"}
		return group
	}
	result := runContext(ctx, m.Probe, "mise", append(nativeStepFor(setupStepID).args, "--dry-run")...)
	if result.Err != nil {
		return group.unavailable(result.Failure())
	}
	preview, changes := nativePreview(result)
	if changes == "" {
		group.State, group.Summary, group.Details = UpdateCurrent, group.Name+": no changes planned", nativeWarnings(preview)
		return group
	}
	summary := summarizeNative(changes)
	group.State, group.Summary = UpdatePending, group.Name
	group.Details = append(summary.Lines, nativeWarnings(preview)...)
	group.Raw, group.Changes, group.Elevated = strings.Split(preview, "\n"), changes, summary.Elevated
	return group
}
