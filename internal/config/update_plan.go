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

// UpdateGroup describes a release or a native maintenance plan.
type UpdateGroup struct {
	Name    string
	State   UpdateState
	Summary string
	Details []string
	Count   int
	// Changes is the part of a native preview that confirmation binds.
	Changes string
}

// UpdatePlan binds confirmation to the release, declarations and native changes.
type UpdatePlan struct {
	CheckedAt       time.Time
	Groups          []UpdateGroup
	Blocked         bool
	ResolvedVersion string
	Scope           UpdateScope
	Configuration   string
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
func (p UpdatePlan) validate(version string) error {
	if p.Blocked {
		return errors.New("Config update is unavailable")
	}
	if !p.Scope.valid() || len(p.Groups) == 0 {
		return errors.New("confirmed update plan is incomplete")
	}
	if (p.Scope == UpdateConfig || p.Scope == UpdateAll) && p.Groups[0].Name != "Config" {
		return errors.New("confirmed Config release plan is incomplete")
	}
	if (p.Scope == UpdateConfig || p.Scope == UpdateAll) && version != "dev" && !stableConfigVersion(p.ResolvedVersion) {
		return errors.New("confirmed plan has no resolved Config release")
	}
	return nil
}
func (p UpdatePlan) Fingerprint() string {
	groups := slices.Clone(p.Groups)
	for index := range groups {
		groups[index].Details = nil
	}
	identity := struct {
		Groups          []UpdateGroup
		Blocked         bool
		ResolvedVersion string
		Scope           UpdateScope
		Configuration   string
	}{groups, p.Blocked, p.ResolvedVersion, p.Scope, p.Configuration}
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
	if scope == UpdateConfig || scope == UpdateAll {
		group, version := u.planConfigRelease(ctx)
		plan.Groups, plan.ResolvedVersion = []UpdateGroup{group}, version
		if group.State == UpdateUnavailable {
			plan.Blocked = true
			return plan, nil
		}
		if group.State == UpdateAvailable && scope == UpdateAll {
			plan.Groups = append(plan.Groups, UpdateGroup{Name: "Machine updates", State: UpdatePending, Summary: "The installed Config release will check machine updates after its verified handoff"})
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
			if scope == UpdateAll {
				plan.Groups = append(plan.Groups, native.plan(ctx, UpdateSoftware)...)
				plan.Groups = append(plan.Groups, native.plan(ctx, UpdateRepositories)...)
			} else {
				plan.Groups = append(plan.Groups, native.plan(ctx, scope)...)
			}
		} else {
			plan.Groups = append(plan.Groups, UpdateGroup{Name: scope.Label(), State: UpdateSkipped, Summary: "No native mise configuration is declared"})
		}
	}
	for _, group := range plan.Groups {
		plan.Blocked = plan.Blocked || group.State == UpdateUnavailable
	}
	return plan, nil
}

func (u Updater) planConfigRelease(ctx context.Context) (UpdateGroup, string) {
	group := UpdateGroup{Name: "Config"}
	if resumedVersion, resumed := os.LookupEnv(UpdateReexecEnv); resumed {
		if resumedVersion != u.Version {
			group.State = UpdateUnavailable
			group.Summary = fmt.Sprintf("release handoff expects %s, but this is %s", resumedVersion, u.Version)
			return group, ""
		}
		if err := u.requireCanonicalExecutable(); err != nil {
			group.State = UpdateUnavailable
			group.Summary = err.Error()
			return group, ""
		}
		group.State = UpdateCurrent
		group.Summary = u.Version + " installed for this update"
		return group, u.Version
	}
	if u.Version == "dev" {
		group.State = UpdateSkipped
		group.Summary = "development build; release discovery skipped"
		return group, ""
	}
	if !stableConfigVersion(u.Version) {
		group.State = UpdateUnavailable
		group.Summary = fmt.Sprintf("build version %q cannot update itself", u.Version)
		return group, ""
	}
	if err := ensureReleaseMiseContext(ctx, u.ReleaseMiseProbe, u.InstallReleaseMise); err != nil {
		group.State = UpdateUnavailable
		group.Summary = "release discovery unavailable: " + err.Error()
		return group, ""
	}
	resolved, err := u.resolveReleaseContext(ctx)
	if err != nil {
		group.State = UpdateUnavailable
		group.Summary = "release discovery unavailable: " + err.Error()
		return group, ""
	}
	comparison := compareConfigVersions(resolved, u.Version)
	switch {
	case comparison < 0:
		group.State = UpdateUnavailable
		group.Summary = fmt.Sprintf("latest release %s is older than installed %s", resolved, u.Version)
		return group, resolved
	case comparison > 0:
		group.State = UpdateAvailable
		group.Count = 1
		group.Summary = u.Version + " → " + resolved
		return group, resolved
	default:
		group.State = UpdateCurrent
		group.Summary = u.Version + " is current"
		if u.requireCanonicalExecutable() != nil {
			group.State = UpdateAvailable
			group.Count = 1
			group.Summary = u.Version + " will be installed at ~/.local/bin/config"
		}
		return group, resolved
	}
}

func WriteUpdatePlan(out io.Writer, plan UpdatePlan) {
	fmt.Fprintln(out, plan.Scope.Label())
	if plan.Blocked {
		fmt.Fprintln(out, "  Update is unavailable")
	}
	for _, group := range plan.Groups {
		fmt.Fprintf(out, "  %s\n", group.Summary)
		for _, detail := range group.Details {
			fmt.Fprintf(out, "    %s\n", detail)
		}
	}
}
