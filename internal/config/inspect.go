package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
)

type Inspector struct {
	Paths           Paths
	Machine         Machine
	Runner          Runner
	Mise            Runner
	Skills          Runner
	FinderFavorites finderFavoritesStore
}

func NewInspector(paths Paths, machine Machine, runner Runner) Inspector {
	return Inspector{
		Paths: paths, Machine: machine, Runner: runner, Mise: NewMiseRunner(paths),
		Skills:          newAgentSkillsRunner(paths),
		FinderFavorites: newFinderFavoritesStore(),
	}
}

func yes(label string) Check {
	return Check{Label: label, OK: true}
}

func no(label, detail string) Check {
	return Check{Label: label, Detail: detail}
}

func authoritativeResource(id, name string, checks []Check) Resource {
	resource := Resource{ID: id, Name: name, Checks: checks, Authoritative: true}
	switch {
	case resource.Failed() > 0:
		resource.State = Drift
		resource.Summary = FormatCount(resource.Failed(), "issue", "issues")
	default:
		resource.State = Current
		resource.Summary = FormatCount(len(checks), "check current", "checks current")
	}
	if resource.State != Current {
		resource.Actions = []Action{Apply}
	}
	return resource
}

func (i Inspector) miseRunner() Runner {
	if i.Mise != nil {
		return i.Mise
	}
	return i.Runner
}

func (i Inspector) agentSkillsRunner() Runner {
	if i.Skills != nil {
		return i.Skills
	}
	return i.Runner
}

func (i Inspector) miseChecks() []Check {
	runner := i.miseRunner()
	return i.miseChecksWithInventory(newMiseRepositoryInventory(i.Paths, runner))
}

func (i Inspector) miseChecksWithInventory(inventory *miseRepositoryInventory) []Check {
	runner := i.miseRunner()
	if !runner.Exists("mise") {
		return []Check{no("mise unavailable at ~/.local/bin/mise", "install the standalone mise binary")}
	}
	currentVersion, err := currentMiseVersion(runner)
	if err != nil {
		return []Check{no("mise version unreadable", "replace the standalone mise binary")}
	}
	if !supportsTestedMise(currentVersion) {
		return []Check{no("mise "+currentVersion+" is unsupported", "install mise "+testedMiseVersion+" at ~/.local/bin/mise")}
	}
	// Every phase probe returns exit 1 for real drift and for a document mise
	// cannot read, so a broken machine document reported as thirteen drifted
	// phases with the true cause buried below them. Ask once, first.
	if listing := run(runner, "mise", "config", "ls", "-J"); listing.Err != nil {
		return []Check{
			yes("mise " + currentVersion),
			no("machine document unreadable", listing.Failure().Error()),
		}
	}
	// The three groups ask mise and git independent questions, so the slowest
	// of them sets the cost rather than their sum.
	var bootstrap, tools, repositories []Check
	var wg sync.WaitGroup
	for _, probe := range []func(){
		func() { bootstrap = i.bootstrapChecks() },
		func() { tools = []Check{i.toolCheck()} },
		func() { repositories = i.repositoryChecks(inventory.Repositories()) },
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			probe()
		}()
	}
	wg.Wait()
	checks := []Check{yes("mise " + currentVersion)}
	checks = append(checks, bootstrap...)
	checks = append(checks, tools...)
	checks = append(checks, repositories...)
	return checks
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
			outcomes[index] = outcome{strings.Join(phase, " "), run(i.miseRunner(), "mise", args...)}
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
		checks = append(checks, no("mise bootstrap unavailable", strings.Join(unavailable, ", ")))
	}
	if len(drifted) > 0 {
		checks = append(checks, no("mise bootstrap state needs attention", strings.Join(drifted, ", ")))
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
	listing := run(i.miseRunner(), "mise", "ls", "--missing", "-J")
	if listing.Err != nil {
		return no("declared tools unreadable", listing.Failure().Error())
	}
	var missing map[string]json.RawMessage
	if err := json.Unmarshal([]byte(listing.Stdout), &missing); err != nil {
		return no("declared tools unreadable", err.Error())
	}
	if len(missing) == 0 {
		return yes("declared tools installed")
	}
	names := slices.Sorted(maps.Keys(missing))
	return no(FormatCount(len(names), "declared tool missing", "declared tools missing"),
		strings.Join(names, ", "))
}

// repositoryChecks answer what [bootstrap.repos] declares: this repository
// belongs at this path. Both halves read locally. How far a checkout has
// drifted from its remote is a different question owned by the repository
// update scope, and one that costs a network round trip for every repository.
func (i Inspector) repositoryChecks(declared []miseRepository, err error) []Check {
	if err != nil {
		return []Check{no("declared repositories unreadable", err.Error())}
	}
	// One git call per declared checkout, so they go out together — but the
	// document decides how many there are, and each one is a process. Bound
	// the fan-out to the machine rather than to the declaration.
	type verdict struct{ absent, foreign bool }
	verdicts := make([]verdict, len(declared))
	tokens := make(chan struct{}, max(1, min(len(declared), runtime.NumCPU())))
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
			tokens <- struct{}{}
			defer func() { <-tokens }()
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
			strings.Join(missing, ", ")))
	}
	if len(foreign) > 0 {
		checks = append(checks, no(FormatCount(len(foreign), "checkout is another repository", "checkouts are another repository"),
			strings.Join(foreign, ", ")))
	}
	if len(checks) > 0 {
		return checks
	}
	return []Check{yes(FormatCount(len(declared), "repository checked out", "repositories checked out"))}
}

func (i Inspector) mise(inventory *miseRepositoryInventory) Resource {
	return authoritativeResource(miseID, miseName, i.miseChecksWithInventory(inventory))
}

func (i Inspector) macOS() Resource {
	return authoritativeResource(macOSID, macOSName,
		setupChecks(i.Paths, i.Runner, macOSFacts(i.Machine)))
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
	// A probe that failed leaves this function's fields at their zero values,
	// which read as a clean tree in agreement with its upstream. Save treats
	// that as success, so an unreadable answer has to become a policy error.
	unreadable := ""
	porcelain := run(runner, "git", "status", "--porcelain=v1", "--untracked-files=all")
	if porcelain.Err != nil {
		unreadable = "working tree state is unreadable"
	}
	for _, line := range strings.Split(strings.TrimSpace(porcelain.Stdout), "\n") {
		if line != "" {
			status.Changes = append(status.Changes, line)
		}
	}
	status.Dirty = len(status.Changes)
	upstream := run(runner, "git", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if upstream.Err == nil {
		status.Upstream = upstream.Output()
		ahead := run(runner, "git", "rev-list", "--count", status.Upstream+"..HEAD")
		behind := run(runner, "git", "rev-list", "--count", "HEAD.."+status.Upstream)
		if ahead.Err != nil || behind.Err != nil {
			if unreadable == "" {
				unreadable = "the distance to " + status.Upstream + " is unreadable"
			}
		} else {
			status.Ahead, _ = strconv.Atoi(ahead.Output())
			status.Behind, _ = strconv.Atoi(behind.Output())
		}
	}
	status.PolicyError = snapshotPolicyError(paths, machine, runner, status)
	if status.PolicyError == "" {
		status.PolicyError = unreadable
	}
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
// snapshot records. Save's gate discards authoritative live resources, so a
// save does not wait for Mise or native macOS probes it cannot use.
func (i Inspector) InspectSnapshot() Report { return i.inspect(false) }

func (i Inspector) inspect(allResources bool) Report {
	bidir := newBidirectional(i.Paths, i.Runner)
	var mise, agentSkills, macOS, chromePWAs, dock, finderFavorites, repositoryHooks Resource
	preferences := make([]Resource, len(i.Machine.Preferences))
	var snapshot SnapshotStatus
	tasks := []func(){
		func() { snapshot = snapshotStatus(i.Paths, i.Machine, i.Runner) },
	}
	var inventory *miseRepositoryInventory
	if allResources {
		if i.Machine.Mise {
			inventory = newMiseRepositoryInventory(i.Paths, i.miseRunner())
			tasks = append(tasks, func() { mise = i.mise(inventory) })
		}
		if i.Machine.AgentSkills != nil {
			tasks = append(tasks, func() {
				agentSkills = inspectAgentSkills(i.Paths, *i.Machine.AgentSkills, i.agentSkillsRunner())
			})
		}
		if len(macOSFacts(i.Machine)) > 0 {
			tasks = append(tasks, func() { macOS = i.macOS() })
		}
		if len(i.Machine.RepositoryHooks) > 0 {
			tasks = append(tasks, func() {
				var repositories []miseRepository
				var err error
				if inventory != nil {
					repositories, err = inventory.Repositories()
				}
				repositoryHooks = inspectRepositoryHooks(i.Paths, i.Machine, i.Runner, repositories, err)
			})
		}
	}
	if i.Machine.FinderFavorites {
		store := i.FinderFavorites
		if store == nil {
			store = newFinderFavoritesStore()
		}
		tasks = append(tasks, func() {
			finderFavorites = bidir.InspectFinderFavorites(store)
		})
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
	resources := make([]Resource, 0, len(preferences)+6)
	if allResources {
		if i.Machine.Mise {
			resources = append(resources, mise)
		}
		if i.Machine.AgentSkills != nil {
			resources = append(resources, agentSkills)
		}
		if len(macOSFacts(i.Machine)) > 0 {
			resources = append(resources, macOS)
		}
	}
	resources = append(resources, preferences...)
	if allResources {
		if len(i.Machine.RepositoryHooks) > 0 {
			resources = append(resources, repositoryHooks)
		}
	}
	if i.Machine.FinderFavorites {
		resources = append(resources, finderFavorites)
	}
	if i.Machine.ChromePWAs {
		resources = append(resources, chromePWAs)
	}
	if i.Machine.Dock {
		resources = append(resources, dock)
	}
	return Report{Resources: resources, Snapshot: snapshot}
}
