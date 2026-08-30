package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
)

type Inspector struct {
	Paths   Paths
	Machine Machine
	Runner  Runner
}

func NewInspector(paths Paths, machine Machine, runner Runner) Inspector {
	return Inspector{Paths: paths, Machine: machine, Runner: runner}
}

func yes(label string) Check {
	return Check{Label: label, OK: true}
}

func no(label string, severity Severity, detail string) Check {
	return Check{Label: label, OK: false, Severity: severity, Detail: detail}
}

func authoritativeResource(id, name string, checks []Check) Resource {
	resource := Resource{ID: id, Name: name, Checks: checks, Authoritative: true}
	switch {
	case resource.Failed() > 0:
		resource.State = Drift
		resource.Summary = FormatCount(resource.Failed(), "issue", "issues")
	case resource.Warned() > 0:
		resource.State = Warning
		resource.Summary = FormatCount(resource.Warned(), "advisory", "advisories")
	default:
		resource.State = Current
		resource.Summary = FormatCount(len(checks), "check current", "checks current")
	}
	if resource.State != Current {
		resource.Actions = []Action{Apply}
	}
	return resource
}

func (i Inspector) substrateChecks() []Check {
	if i.Runner.Exists("git") {
		return []Check{yes("git")}
	}
	return []Check{no("git unavailable", Failure, "install Git")}
}

func (i Inspector) miseChecks() []Check {
	if !i.Runner.Exists("mise") {
		return []Check{no("mise unavailable at ~/.local/bin/mise", Failure, "install the standalone mise binary")}
	}
	version := run(i.Runner, "mise", "--version")
	currentVersion, parsed := miseVersion(version.Stdout)
	if version.Err != nil || !parsed {
		return []Check{no("mise version unreadable", Failure, "replace the standalone mise binary")}
	}
	if !miseVersionAtLeast(version.Stdout, minimumMiseVersion) {
		return []Check{no("mise "+currentVersion+" is too old", Failure, "install "+minimumMiseVersion+" or newer at ~/.local/bin/mise")}
	}
	checks := []Check{yes("mise " + currentVersion)}
	checks = append(checks, i.bootstrapChecks()...)
	checks = append(checks, i.toolCheck())
	checks = append(checks, i.repositoryChecks()...)
	return append(checks, setupChecks(i.Paths, i.Runner, miseFacts(i.Machine))...)
}

// bootstrapChecks probes every declared phase at once. Naming the phases
// costs Config a list to keep current and buys back which phase drifted,
// which the aggregate's single exit code could never say.
func (i Inspector) bootstrapChecks() []Check {
	type outcome struct {
		phase  string
		result Result
	}
	outcomes := make([]outcome, len(misePhases))
	var wg sync.WaitGroup
	for index, phase := range misePhases {
		wg.Add(1)
		go func() {
			defer wg.Done()
			args := append(append([]string{"bootstrap"}, phase...), "status", "--missing")
			outcomes[index] = outcome{strings.Join(phase, " "), run(i.Runner, "mise", args...)}
		}()
	}
	wg.Wait()
	var drifted, unavailable []string
	for _, outcome := range outcomes {
		// A converged phase exits zero, and ExitCode reports -1 for that
		// because there is no ExitError to read. Test the error first, or
		// every healthy phase reads as a missing binary.
		switch {
		case outcome.result.Err == nil:
		case outcome.result.ExitCode() == 1:
			drifted = append(drifted, outcome.phase)
		default:
			unavailable = append(unavailable, outcome.phase)
		}
	}
	var checks []Check
	if len(unavailable) > 0 {
		checks = append(checks, no("mise bootstrap unavailable", Failure, strings.Join(unavailable, ", ")))
	}
	if len(drifted) > 0 {
		checks = append(checks, no("mise bootstrap state needs attention", Failure, strings.Join(drifted, ", ")))
	}
	if len(checks) == 0 {
		checks = append(checks, yes("mise bootstrap state"))
	}
	return checks
}

// toolCheck covers the one declared category with no bootstrap phase of its
// own. `mise ls --missing` exits zero either way, so a complete machine is an
// empty listing rather than a successful command.
func (i Inspector) toolCheck() Check {
	listing := run(i.Runner, "mise", "ls", "--missing", "-J")
	if listing.Err != nil {
		return no("declared tools unreadable", Failure, listing.Failure().Error())
	}
	var missing map[string]json.RawMessage
	if err := json.Unmarshal([]byte(listing.Stdout), &missing); err != nil {
		return no("declared tools unreadable", Failure, err.Error())
	}
	if len(missing) == 0 {
		return yes("declared tools installed")
	}
	names := slices.Sorted(maps.Keys(missing))
	return no(FormatCount(len(names), "declared tool missing", "declared tools missing"),
		Failure, strings.Join(names, ", "))
}

// repositoryChecks answer what [bootstrap.repos] declares: this repository
// belongs at this path. Both halves read locally. How far a checkout has
// drifted from its remote is a different question, one config update owns
// and one that costs a network round trip for every repository.
func (i Inspector) repositoryChecks() []Check {
	declared, err := miseRepositories(i.Paths, i.Runner)
	if err != nil {
		return []Check{no("declared repositories unreadable", Failure, err.Error())}
	}
	// One git call per declared checkout, so they go out together.
	type verdict struct{ absent, foreign bool }
	verdicts := make([]verdict, len(declared))
	var wg sync.WaitGroup
	for index, repository := range declared {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, statErr := os.Stat(filepath.Join(repository.Path, ".git")); statErr != nil {
				verdicts[index] = verdict{absent: true}
				return
			}
			if repository.URL == "" {
				return
			}
			origin := run(i.Runner, "git", "-C", repository.Path, "remote", "get-url", managedRemote)
			verdicts[index] = verdict{foreign: origin.Err != nil || !sameRepositoryLocator(origin.Output(), repository.URL)}
		}()
	}
	wg.Wait()
	var missing, foreign []string
	for index, repository := range declared {
		switch name := filepath.Base(repository.Path); {
		case verdicts[index].absent:
			missing = append(missing, name)
		case verdicts[index].foreign:
			foreign = append(foreign, name)
		}
	}
	var checks []Check
	if len(missing) > 0 {
		checks = append(checks, no(FormatCount(len(missing), "declared repository missing", "declared repositories missing"),
			Failure, strings.Join(missing, ", ")))
	}
	if len(foreign) > 0 {
		checks = append(checks, no(FormatCount(len(foreign), "checkout is another repository", "checkouts are another repository"),
			Failure, strings.Join(foreign, ", ")))
	}
	if len(checks) > 0 {
		return checks
	}
	return []Check{yes(FormatCount(len(declared), "repository checked out", "repositories checked out"))}
}

func (i Inspector) setup() Resource {
	checks := i.substrateChecks()
	checks = append(checks, i.miseChecks()...)
	return authoritativeResource(setupID, setupName, checks)
}

// snapshotStatus is the one derivation of repository snapshot facts; the
// inspector reports it and the snapshotter saves against it.
func snapshotStatus(paths Paths, machine Machine, runner Runner) SnapshotStatus {
	status := SnapshotStatus{Branch: "detached", Commit: "none", Destination: machine.Repository.Destination()}
	if branch := run(runner, "git", "symbolic-ref", "--quiet", "--short", "HEAD"); branch.Err == nil {
		status.Branch = branch.Output()
	}
	if commit := run(runner, "git", "rev-parse", "--short", "HEAD"); commit.Err == nil {
		status.Commit = commit.Output()
	}
	porcelain := run(runner, "git", "status", "--porcelain=v1", "--untracked-files=all")
	for _, line := range strings.Split(strings.TrimSpace(porcelain.Stdout), "\n") {
		if line != "" {
			status.Changes = append(status.Changes, line)
		}
	}
	status.Dirty = len(status.Changes)
	upstream := run(runner, "git", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if upstream.Err == nil {
		status.Upstream = upstream.Output()
		if ahead := run(runner, "git", "rev-list", "--count", status.Upstream+"..HEAD"); ahead.Err == nil {
			status.Ahead, _ = strconv.Atoi(ahead.Output())
		}
		if behind := run(runner, "git", "rev-list", "--count", "HEAD.."+status.Upstream); behind.Err == nil {
			status.Behind, _ = strconv.Atoi(behind.Output())
		}
	}
	status.PolicyError = snapshotPolicyError(paths, machine, runner, status)
	return status
}

func snapshotPolicyError(paths Paths, machine Machine, runner Runner, status SnapshotStatus) string {
	top := run(runner, "git", "rev-parse", "--show-toplevel")
	if top.Err != nil || !samePath(top.Output(), paths.Root) {
		return "repository root does not match Config's managed checkout"
	}
	if status.Branch != machine.Repository.Branch {
		return fmt.Sprintf("branch is %s; expected %s", status.Branch, machine.Repository.Branch)
	}
	wantUpstream := machine.Repository.Destination()
	if status.Upstream != wantUpstream {
		if status.Upstream == "" {
			return "branch has no upstream; expected " + wantUpstream
		}
		return fmt.Sprintf("upstream is %s; expected %s", status.Upstream, wantUpstream)
	}
	remote := run(runner, "git", "remote", "get-url", managedRemote)
	if remote.Err != nil {
		return "remote " + managedRemote + " is unavailable"
	}
	if !sameRepositoryLocator(remote.Output(), machine.Repository.URL) {
		return fmt.Sprintf("remote %s is not %s", managedRemote, machine.Repository.URL)
	}
	return ""
}

func (i Inspector) Inspect() Report { return i.inspect(true) }

// InspectSnapshot reports only the resources whose state the repository
// snapshot records. Save's gate discards machine setup, and machine setup is
// the probe that reaches the network, so a save should not wait for it.
func (i Inspector) InspectSnapshot() Report { return i.inspect(false) }

func (i Inspector) inspect(machineSetup bool) Report {
	bidir := NewBidirectional(i.Paths, i.Runner)
	var setup, chromePWAs, dock Resource
	preferences := make([]Resource, len(i.Machine.Preferences))
	var snapshot SnapshotStatus
	tasks := []func(){
		func() { snapshot = snapshotStatus(i.Paths, i.Machine, i.Runner) },
	}
	if machineSetup {
		tasks = append(tasks, func() { setup = i.setup() })
	}
	if i.Machine.ChromePWAs {
		tasks = append(tasks, func() { chromePWAs = bidir.InspectChromePWAs() })
	}
	if i.Machine.Dock {
		tasks = append(tasks, func() { dock = bidir.InspectDock() })
	}
	for index, preference := range i.Machine.Preferences {
		tasks = append(tasks, func() { preferences[index] = preference.Inspect(i.Paths) })
	}
	var wg sync.WaitGroup
	for _, task := range tasks {
		wg.Add(1)
		go func(task func()) {
			defer wg.Done()
			task()
		}(task)
	}
	wg.Wait()
	resources := slices.Clone(preferences)
	if machineSetup {
		resources = append([]Resource{setup}, resources...)
	}
	if i.Machine.ChromePWAs {
		resources = append(resources, chromePWAs)
	}
	if i.Machine.Dock {
		resources = append(resources, dock)
	}
	return Report{Resources: resources, Snapshot: snapshot}
}
