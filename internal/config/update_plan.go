package config

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

type UpdateState string

const (
	UpdateCurrent     UpdateState = "current"
	UpdateAvailable   UpdateState = "available"
	UpdateUnavailable UpdateState = "unavailable"
	UpdateSkipped     UpdateState = "skipped"
)

// UpdateGroup describes Config's own release availability.
type UpdateGroup struct {
	Name    string
	State   UpdateState
	Summary string
	Details []string
	Count   int
}

// UpdatePlan binds confirmation to the exact Config release shown to the caller.
type UpdatePlan struct {
	CheckedAt       time.Time
	Groups          []UpdateGroup
	Blocked         bool
	ResolvedVersion string
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
func (p UpdatePlan) HasWork() bool { n, _ := p.Counts(); return !p.Blocked && n > 0 }
func (p UpdatePlan) validate(version string) error {
	if p.Blocked {
		return errors.New("Config update is unavailable")
	}
	if len(p.Groups) != 1 || p.Groups[0].Name != "Config" {
		return errors.New("confirmed update plan is incomplete")
	}
	if version != "dev" && !stableConfigVersion(p.ResolvedVersion) {
		return errors.New("confirmed plan has no resolved Config release")
	}
	return nil
}
func (p UpdatePlan) Fingerprint() string {
	identity := struct {
		Groups          []UpdateGroup
		Blocked         bool
		ResolvedVersion string
	}{p.Groups, p.Blocked, p.ResolvedVersion}
	data, _ := json.Marshal(identity)
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data))
}
func (u Updater) Plan() (UpdatePlan, error) { return u.PlanContext(context.Background()) }
func (u Updater) PlanContext(ctx context.Context) (UpdatePlan, error) {
	group, version := u.planConfigRelease(ctx)
	return UpdatePlan{CheckedAt: time.Now(), Groups: []UpdateGroup{group}, Blocked: group.State == UpdateUnavailable, ResolvedVersion: version}, nil
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
	fmt.Fprintln(out, "Config update")
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
