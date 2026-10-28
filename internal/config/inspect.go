package config

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

type Inspector struct {
	Paths   Paths
	Machine Machine
	Runner  Runner

	FinderFavorites finderFavoritesStore
}

func NewInspector(paths Paths, machine Machine, runner Runner) Inspector {
	return Inspector{
		Paths: paths, Machine: machine, Runner: runner,
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
// save does not wait for native macOS probes it cannot use.
func (i Inspector) InspectSnapshot() Report { return i.inspect(false) }

func (i Inspector) inspect(allResources bool) Report {
	bidir := newBidirectional(i.Paths)
	var macOS, chromePWAs, finderFavorites Resource
	preferences := make([]Resource, len(i.Machine.Preferences))
	var snapshot SnapshotStatus
	tasks := []func(){
		func() { snapshot = snapshotStatus(i.Paths, i.Machine, i.Runner) },
	}
	if allResources {
		if len(macOSFacts(i.Machine)) > 0 {
			tasks = append(tasks, func() { macOS = i.macOS() })
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
		if len(macOSFacts(i.Machine)) > 0 {
			resources = append(resources, macOS)
		}
	}
	resources = append(resources, preferences...)
	if i.Machine.FinderFavorites {
		resources = append(resources, finderFavorites)
	}
	if i.Machine.ChromePWAs {
		resources = append(resources, chromePWAs)
	}
	return Report{Resources: resources, Snapshot: snapshot}
}
