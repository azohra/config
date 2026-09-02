package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// UpdateState says how much Config knows before an update mutates the Mac.
// Pending is deliberate: some providers can update their declarations but do
// not yet expose exact, machine-readable discovery.
type UpdateState string

const (
	UpdateCurrent     UpdateState = "current"
	UpdateAvailable   UpdateState = "available"
	UpdatePending     UpdateState = "pending"
	UpdateUnavailable UpdateState = "unavailable"
	UpdateSkipped     UpdateState = "skipped"
)

// UpdateGroup is one provider-owned part of an update preview. Scope is All
// for work, such as Config and Mise compatibility, that precedes either
// selected machine scope.
type UpdateGroup struct {
	Name    string
	Scope   UpdateScope
	State   UpdateState
	Summary string
	Details []string
	Count   int
}

// UpdatePlan is the immutable answer shown before an update. A blocked plan
// cannot safely cross Config's release boundary; pending groups remain
// runnable because their provider can only answer while applying.
type UpdatePlan struct {
	Scope     UpdateScope
	CheckedAt time.Time
	Groups    []UpdateGroup
	Blocked   bool
}

func (p UpdatePlan) GroupsFor(scope UpdateScope) []UpdateGroup {
	var groups []UpdateGroup
	for _, group := range p.Groups {
		if group.Scope == UpdateAll || scope == UpdateAll || group.Scope == scope {
			groups = append(groups, group)
		}
	}
	return groups
}

func (p UpdatePlan) Counts(scope UpdateScope) (available, pending, unavailable int) {
	for _, group := range p.GroupsFor(scope) {
		switch group.State {
		case UpdateAvailable:
			available += max(1, group.Count)
		case UpdatePending:
			pending++
		case UpdateUnavailable:
			unavailable++
		}
	}
	return available, pending, unavailable
}

func (p UpdatePlan) HasWork() bool {
	if p.Blocked {
		return false
	}
	available, pending, unavailable := p.Counts(p.Scope)
	return available+pending+unavailable > 0
}

// Plan discovers everything the current Config release can prove without
// changing declared machine state. Its own private release adapter may be
// prepared so Config can answer whether a newer Config exists.
func (u Updater) Plan(scope UpdateScope) (UpdatePlan, error) {
	if !scope.valid() {
		return UpdatePlan{}, fmt.Errorf("invalid update scope")
	}
	plan := UpdatePlan{Scope: scope, CheckedAt: time.Now()}
	configGroup, currentRelease := u.planConfigRelease()
	plan.Groups = append(plan.Groups, configGroup)
	if configGroup.State == UpdateUnavailable {
		plan.Blocked = true
		return plan, nil
	}
	if !currentRelease {
		plan.Groups = append(plan.Groups, UpdateGroup{
			Name: "Machine update", Scope: scope, State: UpdatePending,
			Summary: "checked by the new Config release before it changes the Mac",
		})
		return plan, nil
	}

	machine, err := u.LoadMachine()
	if err != nil {
		return UpdatePlan{}, err
	}
	if machine.Mise {
		miseGroup, ready := u.planMise()
		plan.Groups = append(plan.Groups, miseGroup)
		if ready {
			if scope == UpdateAll || scope == UpdateSoftware {
				plan.Groups = append(plan.Groups, u.planTools())
			}
			if scope == UpdateAll || scope == UpdateRepositories {
				plan.Groups = append(plan.Groups, u.planRepositories())
			}
		} else {
			if scope == UpdateAll || scope == UpdateSoftware {
				plan.Groups = append(plan.Groups, UpdateGroup{
					Name: "Tools", Scope: UpdateSoftware, State: UpdatePending,
					Summary: "checked after Mise compatibility is restored",
				})
			}
			if scope == UpdateAll || scope == UpdateRepositories {
				plan.Groups = append(plan.Groups, UpdateGroup{
					Name: "Repositories", Scope: UpdateRepositories, State: UpdatePending,
					Summary: "checked after Mise compatibility is restored",
				})
			}
		}
		if scope == UpdateAll || scope == UpdateSoftware {
			plan.Groups = append(plan.Groups, u.planPackages())
		}
	}
	if machine.AgentSkills != nil && (scope == UpdateAll || scope == UpdateSoftware) {
		count := len(machine.AgentSkills.desired())
		plan.Groups = append(plan.Groups, UpdateGroup{
			Name: "Agent skills", Scope: UpdateSoftware, State: UpdatePending,
			Summary: fmt.Sprintf("%s checked by the skills adapter when run", FormatCount(count, "declared skill", "declared skills")),
		})
	}
	return plan, nil
}

func (u Updater) planConfigRelease() (UpdateGroup, bool) {
	group := UpdateGroup{Name: "Config", Scope: UpdateAll}
	if resumedVersion, resumed := os.LookupEnv(updateReexecEnv); resumed {
		if resumedVersion != u.Version {
			group.State = UpdateUnavailable
			group.Summary = fmt.Sprintf("release handoff expects %s, but this is %s", resumedVersion, u.Version)
			return group, false
		}
		group.State = UpdateCurrent
		group.Summary = u.Version + " installed for this update"
		return group, true
	}
	if u.Version == "dev" {
		group.State = UpdateSkipped
		group.Summary = "development build; release discovery skipped"
		return group, true
	}
	if !stableConfigVersion(u.Version) {
		group.State = UpdateUnavailable
		group.Summary = fmt.Sprintf("build version %q cannot update itself", u.Version)
		return group, false
	}
	if err := ensureTestedMise(u.ReleaseMiseProbe, u.InstallReleaseMise); err != nil {
		group.State = UpdateUnavailable
		group.Summary = "release discovery unavailable: " + err.Error()
		return group, false
	}
	resolved, err := u.resolveRelease()
	if err != nil {
		group.State = UpdateUnavailable
		group.Summary = "release discovery unavailable: " + err.Error()
		return group, false
	}
	comparison := compareConfigVersions(resolved, u.Version)
	switch {
	case comparison < 0:
		group.State = UpdateUnavailable
		group.Summary = fmt.Sprintf("latest release %s is older than installed %s", resolved, u.Version)
		return group, false
	case comparison > 0:
		group.State = UpdateAvailable
		group.Count = 1
		group.Summary = u.Version + " → " + resolved
		return group, false
	default:
		group.State = UpdateCurrent
		group.Summary = u.Version + " is current"
		return group, true
	}
}

func (u Updater) planMise() (UpdateGroup, bool) {
	group := UpdateGroup{Name: miseName, Scope: UpdateAll}
	if !u.MachineMiseProbe.Exists("mise") {
		group.State = UpdateAvailable
		group.Count = 1
		group.Summary = "not installed → " + testedMiseVersion
		return group, false
	}
	version, err := currentMiseVersion(u.MachineMiseProbe)
	if err != nil {
		group.State = UpdateAvailable
		group.Count = 1
		group.Summary = "unreadable installation → " + testedMiseVersion
		return group, false
	}
	if version != testedMiseVersion {
		group.State = UpdateAvailable
		group.Count = 1
		group.Summary = version + " → " + testedMiseVersion
		return group, false
	}
	group.State = UpdateCurrent
	group.Summary = version + " is compatible"
	return group, true
}

func (u Updater) planTools() UpdateGroup {
	group := UpdateGroup{Name: "Tools", Scope: UpdateSoftware}
	result := run(u.MachineMisePlan, "mise", "outdated", "--json")
	if result.Err != nil {
		group.State = UpdateUnavailable
		group.Summary = result.Failure().Error()
		return group
	}
	var tools map[string]json.RawMessage
	if err := json.Unmarshal([]byte(result.Stdout), &tools); err != nil {
		group.State = UpdateUnavailable
		group.Summary = "Mise returned unreadable update data"
		return group
	}
	if len(tools) == 0 {
		group.State = UpdateCurrent
		group.Summary = "declared tools are current"
		return group
	}
	group.State = UpdateAvailable
	group.Count = len(tools)
	group.Summary = FormatCount(len(tools), "tool update", "tool updates")
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		var versions struct {
			Current string `json:"current"`
			Latest  string `json:"latest"`
		}
		_ = json.Unmarshal(tools[name], &versions)
		detail := name
		if versions.Current != "" && versions.Latest != "" {
			detail += " " + versions.Current + " → " + versions.Latest
		}
		group.Details = append(group.Details, detail)
	}
	return group
}

func (u Updater) planPackages() UpdateGroup {
	group := UpdateGroup{Name: "Packages", Scope: UpdateSoftware}
	result := run(u.MachineMisePlan, "mise", "bootstrap", "packages", "status", "--json")
	if result.Err != nil {
		group.State = UpdateUnavailable
		group.Summary = result.Failure().Error()
		return group
	}
	var managers map[string]struct {
		Packages []json.RawMessage `json:"packages"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &managers); err != nil {
		group.State = UpdateUnavailable
		group.Summary = "Mise returned unreadable package data"
		return group
	}
	packages := 0
	for _, manager := range managers {
		packages += len(manager.Packages)
	}
	if packages == 0 {
		group.State = UpdateCurrent
		group.Summary = "no packages are declared"
		return group
	}
	group.State = UpdatePending
	group.Summary = fmt.Sprintf("%s across %s; exact updates are checked when run",
		FormatCount(packages, "declared package", "declared packages"),
		FormatCount(len(managers), "manager", "managers"))
	return group
}

func (u Updater) planRepositories() UpdateGroup {
	group := UpdateGroup{Name: "Repositories", Scope: UpdateRepositories}
	result := run(u.MachineMisePlan, "mise", "bootstrap", "repos", "status", "--json")
	if result.Err != nil {
		group.State = UpdateUnavailable
		group.Summary = result.Failure().Error()
		return group
	}
	var status struct {
		Repositories []struct {
			Path   string `json:"path"`
			State  string `json:"state"`
			Reason string `json:"reason"`
		} `json:"repos"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &status); err != nil {
		group.State = UpdateUnavailable
		group.Summary = "Mise returned unreadable repository data"
		return group
	}
	for _, repository := range status.Repositories {
		if repository.State == "current" {
			continue
		}
		detail := filepath.Base(repository.Path) + ": " + repository.State
		if repository.Reason != "" {
			detail += " — " + repository.Reason
		}
		group.Details = append(group.Details, detail)
	}
	if len(group.Details) > 0 {
		group.State = UpdateAvailable
		group.Count = len(group.Details)
		group.Summary = FormatCount(len(group.Details), "repository needs attention", "repositories need attention")
		return group
	}
	if len(status.Repositories) == 0 {
		group.State = UpdateCurrent
		group.Summary = "no repositories are declared"
		return group
	}
	group.State = UpdatePending
	group.Summary = fmt.Sprintf("%s locally current; remote freshness is checked when run", FormatCount(len(status.Repositories), "repository", "repositories"))
	return group
}

// WriteUpdatePlan renders one shared preview for the CLI and terminal app.
func WriteUpdatePlan(out io.Writer, plan UpdatePlan) {
	label := "all"
	switch plan.Scope {
	case UpdateSoftware:
		label = "software"
	case UpdateRepositories:
		label = "repositories"
	}
	fmt.Fprintf(out, "Update plan · %s\n", label)
	for _, group := range plan.GroupsFor(plan.Scope) {
		symbol := GlyphInfo
		switch group.State {
		case UpdateCurrent:
			symbol = GlyphOK
		case UpdateAvailable:
			symbol = GlyphInfo
		case UpdatePending, UpdateSkipped:
			symbol = GlyphWarn
		case UpdateUnavailable:
			symbol = GlyphError
		}
		fmt.Fprintf(out, "  %s %-16s %s\n", symbol, group.Name, group.Summary)
		for _, detail := range group.Details {
			fmt.Fprintln(out, "      "+strings.TrimSpace(detail))
		}
	}
	if plan.Blocked {
		fmt.Fprintln(out, "\nUpdate is unavailable until Config release discovery succeeds.")
	} else if !plan.HasWork() {
		fmt.Fprintln(out, "\nEverything checked is current.")
	}
}
