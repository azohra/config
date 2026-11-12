package config

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
)

type UpdateState string

const (
	UpdateCurrent     UpdateState = "current"
	UpdateAvailable   UpdateState = "available"
	UpdateUnavailable UpdateState = "unavailable"
	UpdateSkipped     UpdateState = "skipped"
	UpdatePending     UpdateState = "pending"
)

// UpdateItem is one thing an update changes: a tool, an app or a checkout.
type UpdateItem struct {
	Name string `json:"name"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	Note string `json:"note,omitempty"`
	// Target selects this item in its step's native command.
	Target string `json:"target"`
}

// Label reads as "node 24.1.0 → 24.2.0".
func (i UpdateItem) Label() string {
	label := i.Name
	switch {
	case i.From != "" && i.To != "":
		label += " " + i.From + " → " + i.To
	case i.From != "" || i.To != "":
		label += " " + i.From + i.To
	}
	if i.Note != "" {
		label += " (" + i.Note + ")"
	}
	return label
}

// UpdateGroup is one step of an update: a release or one native command, and
// the items it changes.
type UpdateGroup struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	State   UpdateState  `json:"state"`
	Summary string       `json:"summary"`
	Items   []UpdateItem `json:"items,omitempty"`
	// Details are notes about the step: warnings and what it leaves alone.
	Details []string `json:"details,omitempty"`
	Count   int      `json:"-"`
	// Command is the native command the step runs.
	Command string `json:"command,omitempty"`
	// Raw is mise's own preview of the step.
	Raw []string `json:"preview,omitempty"`
	// Changes is the part of a native preview that confirmation binds when the
	// step has no items.
	Changes string `json:"-"`
	// Elevated is true when the step runs something as root.
	Elevated bool `json:"elevated,omitempty"`
	// Partial is true when a selection dropped some of the step's items.
	Partial bool `json:"partial,omitempty"`
}

// Key identifies an item across plans and in a selection.
func (g UpdateGroup) Key(item UpdateItem) string { return g.ID + ":" + item.Target }

// itemized fills a step's state and summary from its items.
func (g UpdateGroup) itemized() UpdateGroup {
	g.Count = len(g.Items)
	if g.Count == 0 {
		g.State, g.Summary = UpdateCurrent, g.Name+": no changes planned"
		return g
	}
	g.State, g.Summary = UpdateAvailable, g.Name+": "+FormatCount(g.Count, "update", "updates")
	return g
}

// UpdatePlan binds confirmation to the release, declarations and native changes.
type UpdatePlan struct {
	CheckedAt       time.Time     `json:"checked_at"`
	Groups          []UpdateGroup `json:"steps"`
	Blocked         bool          `json:"blocked,omitempty"`
	ResolvedVersion string        `json:"-"`
	Scope           UpdateScope   `json:"scope"`
	Configuration   string        `json:"-"`
}

func (p UpdatePlan) Counts() (available, unavailable int) {
	for _, g := range p.Groups {
		switch g.State {
		case UpdateAvailable:
			available += max(1, g.Count)
		case UpdateUnavailable:
			unavailable++
		}
	}
	return
}

// Elevated reports whether the plan runs a native step as root.
func (p UpdatePlan) Elevated() bool {
	for _, group := range p.Groups {
		if group.Elevated && group.State != UpdateSkipped {
			return true
		}
	}
	return false
}

func (p UpdatePlan) HasWork() bool {
	if p.Blocked {
		return false
	}
	for _, group := range p.Groups {
		if group.State == UpdateAvailable || group.State == UpdatePending {
			return true
		}
	}
	return false
}

// Select keeps the named items and leaves every other step out. A name is a
// step's id, or an item's key, name, target or the last part of its target.
// A step's id selects that step and nothing else; a name that items in
// several steps share is refused, so a selection never runs more than its
// words say.
func (p UpdatePlan) Select(names []string) (UpdatePlan, error) {
	if len(names) == 0 {
		return p, nil
	}
	steps := map[string]bool{}
	for _, group := range p.Groups {
		steps[strings.ToLower(group.ID)] = true
	}
	kept := make([][]UpdateItem, len(p.Groups))
	whole := make([]bool, len(p.Groups))
	for _, name := range names {
		var keys []string
		for index, group := range p.Groups {
			if strings.EqualFold(name, group.ID) {
				whole[index] = true
				keys = append(keys, group.ID)
			}
			if steps[strings.ToLower(name)] {
				continue
			}
			for _, item := range group.Items {
				if item.named(group, name) {
					keys = append(keys, group.Key(item))
					if !slices.Contains(kept[index], item) {
						kept[index] = append(kept[index], item)
					}
				}
			}
		}
		switch {
		case len(keys) == 0 && p.awaitsHandoff():
			return UpdatePlan{}, fmt.Errorf("%q is checked after the new Config installs; run `config update config --yes` first", name)
		case len(keys) == 0:
			return UpdatePlan{}, fmt.Errorf("no pending update is named %q", name)
		case len(keys) > 1:
			return UpdatePlan{}, fmt.Errorf("%q names more than one update; choose %s", name, strings.Join(keys, " or "))
		}
	}
	groups := slices.Clone(p.Groups)
	for index, group := range groups {
		switch {
		case whole[index]:
		case len(kept[index]) == 0:
			groups[index] = UpdateGroup{ID: group.ID, Name: group.Name, State: UpdateSkipped, Summary: group.Name + ": not selected"}
		case len(kept[index]) < len(group.Items):
			// Keep the plan's order, whatever order the names came in.
			group.Items = slices.DeleteFunc(slices.Clone(group.Items), func(item UpdateItem) bool { return !slices.Contains(kept[index], item) })
			group.Partial = true
			if step := nativeStepFor(group.ID); step.id != "" {
				group.Command = "mise " + strings.Join(step.command(group), " ")
			}
			groups[index] = group.itemized()
		}
	}
	p.Groups = groups
	// Machine updates wait for the release that checks them.
	if p.awaitsHandoff() && p.Groups[0].State == UpdateSkipped {
		return UpdatePlan{}, errors.New("machine updates are checked after the new Config installs; select config as well")
	}
	return p, nil
}

// awaitsHandoff reports whether machine updates wait for a Config release.
func (p UpdatePlan) awaitsHandoff() bool {
	return slices.ContainsFunc(p.Groups, func(group UpdateGroup) bool {
		return group.ID == machineStepID && group.State == UpdatePending
	})
}

func (i UpdateItem) named(group UpdateGroup, name string) bool {
	short := i.Target
	if at := strings.LastIndexAny(short, ":/"); at >= 0 {
		short = short[at+1:]
	}
	for _, candidate := range []string{group.Key(i), i.Name, i.Target, short} {
		if candidate != "" && strings.EqualFold(candidate, name) {
			return true
		}
	}
	return false
}

func (p UpdatePlan) validate(version string) error {
	if p.Blocked {
		return errors.New("Config update is unavailable")
	}
	if !p.Scope.valid() || len(p.Groups) == 0 {
		return errors.New("confirmed update plan is incomplete")
	}
	if p.Scope.includesConfig() && p.Groups[0].ID != configStepID {
		return errors.New("confirmed Config release plan is incomplete")
	}
	if p.Scope.includesConfig() && version != "dev" && !stableConfigVersion(p.ResolvedVersion) {
		return errors.New("confirmed plan has no resolved Config release")
	}
	return nil
}

// Fingerprint is the identity a confirmation binds: the steps, their items and
// the declarations, not the notes or the time of the check.
func (p UpdatePlan) Fingerprint() string {
	groups := slices.Clone(p.Groups)
	for index := range groups {
		groups[index].Details, groups[index].Raw, groups[index].Command = nil, nil, ""
	}
	identity := struct {
		Groups          []UpdateGroup
		Changes         []string
		Blocked         bool
		ResolvedVersion string
		Scope           UpdateScope
		Configuration   string
	}{Groups: groups, Blocked: p.Blocked, ResolvedVersion: p.ResolvedVersion, Scope: p.Scope, Configuration: p.Configuration}
	for _, group := range groups {
		identity.Changes = append(identity.Changes, group.Changes)
	}
	data, _ := json.Marshal(identity)
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data))
}

func (u Updater) Plan(scope ...UpdateScope) (UpdatePlan, error) {
	return u.PlanContext(context.Background(), scope...)
}

func (u Updater) PlanContext(ctx context.Context, selected ...UpdateScope) (UpdatePlan, error) {
	scope := UpdateConfig
	if len(selected) > 0 {
		scope = selected[0]
	}
	if !scope.valid() {
		return UpdatePlan{}, errors.New("unknown update scope")
	}
	plan := UpdatePlan{CheckedAt: time.Now(), Scope: scope}
	if scope.includesConfig() {
		group, version := u.planConfigRelease(ctx)
		plan.Groups, plan.ResolvedVersion = []UpdateGroup{group}, version
		if group.State == UpdateUnavailable {
			plan.Blocked = true
			return plan, nil
		}
		if group.State == UpdateAvailable && scope == UpdateAll {
			plan.Groups = append(plan.Groups, UpdateGroup{
				ID: machineStepID, Name: "Machine updates", State: UpdatePending,
				Summary: "Machine updates: checked by the new Config after its verified handoff",
			})
			return plan, nil
		}
	}
	if scope != UpdateConfig {
		_, err := LoadMachine(u.Paths)
		if err != nil {
			return UpdatePlan{}, err
		}
		if machineUsesMise(u.Paths) {
			native := u.nativeMise()
			plan.Configuration, err = native.configurationDigest(ctx)
			if err != nil {
				return UpdatePlan{}, err
			}
			plan.Groups = append(plan.Groups, native.plan(ctx, scope)...)
		} else {
			plan.Groups = append(plan.Groups, UpdateGroup{ID: machineStepID, Name: scope.Label(), State: UpdateSkipped, Summary: "No native mise configuration is declared"})
		}
	}
	for _, group := range plan.Groups {
		plan.Blocked = plan.Blocked || group.State == UpdateUnavailable
	}
	return plan, nil
}

func (u Updater) planConfigRelease(ctx context.Context) (UpdateGroup, string) {
	group := UpdateGroup{ID: configStepID, Name: "Config"}
	if resumedVersion, resumed := os.LookupEnv(UpdateReexecEnv); resumed {
		if resumedVersion != u.Version {
			group.State = UpdateUnavailable
			group.Summary = fmt.Sprintf("Config: release handoff expects %s, but this is %s", resumedVersion, u.Version)
			return group, ""
		}
		if err := u.requireCanonicalExecutable(); err != nil {
			group.State = UpdateUnavailable
			group.Summary = "Config: " + err.Error()
			return group, ""
		}
		group.State = UpdateCurrent
		group.Summary = "Config: " + u.Version + " installed for this update"
		return group, u.Version
	}
	if u.Version == "dev" {
		group.State = UpdateSkipped
		group.Summary = "Config: development build; release discovery skipped"
		return group, ""
	}
	if !stableConfigVersion(u.Version) {
		group.State = UpdateUnavailable
		group.Summary = fmt.Sprintf("Config: build version %q cannot update itself", u.Version)
		return group, ""
	}
	if err := ensureReleaseMiseContext(ctx, u.ReleaseMiseProbe, u.InstallReleaseMise); err != nil {
		group.State = UpdateUnavailable
		group.Summary = "Config: release discovery unavailable: " + err.Error()
		return group, ""
	}
	resolved, err := u.resolveReleaseContext(ctx)
	if err != nil {
		group.State = UpdateUnavailable
		group.Summary = "Config: release discovery unavailable: " + err.Error()
		return group, ""
	}
	comparison := compareConfigVersions(resolved, u.Version)
	switch {
	case comparison < 0:
		group.State = UpdateUnavailable
		group.Summary = fmt.Sprintf("Config: latest release %s is older than installed %s", resolved, u.Version)
		return group, resolved
	case comparison > 0:
		group.State = UpdateAvailable
		group.Count = 1
		group.Summary = "Config: " + u.Version + " → " + resolved
		group.Items = []UpdateItem{{Name: "Config", From: u.Version, To: resolved, Target: configStepID}}
		return group, resolved
	default:
		group.State = UpdateCurrent
		group.Summary = "Config: " + u.Version + " is current"
		if u.requireCanonicalExecutable() != nil {
			group.State = UpdateAvailable
			group.Count = 1
			group.Summary = "Config: " + u.Version + " will be installed at ~/.local/bin/config"
			group.Items = []UpdateItem{{Name: "Config", To: u.Version, Note: "install at ~/.local/bin/config", Target: configStepID}}
		}
		return group, resolved
	}
}

// WriteUpdatePlan renders each step, the items it changes and its notes.
func WriteUpdatePlan(out io.Writer, plan UpdatePlan) {
	fmt.Fprintln(out, plan.Scope.Label())
	if plan.Blocked {
		fmt.Fprintln(out, "  Update is unavailable")
	}
	for _, group := range plan.Groups {
		fmt.Fprintf(out, "  %s\n", group.Summary)
		for _, item := range group.Items {
			// A step that is its own single item says it all in its summary.
			if len(group.Items) > 1 || item.Name != group.Name {
				fmt.Fprintf(out, "    %s\n", item.Label())
			}
		}
		for _, detail := range group.Details {
			fmt.Fprintf(out, "    %s\n", detail)
		}
	}
}

// WriteUpdateCommands renders mise's own preview for each native step.
func WriteUpdateCommands(out io.Writer, plan UpdatePlan) {
	fmt.Fprintln(out, plan.Scope.Label()+": mise's commands")
	for _, group := range plan.Groups {
		if group.Command == "" && len(group.Raw) == 0 || group.State != UpdateAvailable && group.State != UpdatePending {
			continue
		}
		fmt.Fprintf(out, "  %s\n", group.Name)
		if group.Command != "" {
			fmt.Fprintf(out, "    %s\n", group.Command)
		}
		for _, line := range group.Raw {
			fmt.Fprintf(out, "    %s\n", line)
		}
	}
}
