package config

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type updatePlanRunner struct {
	answers map[string]Result
	exists  bool
}

func TestUpdatePlanDefersMachineDiscoveryToANewerConfigRelease(t *testing.T) {
	paths := testPaths(t)
	writeUpdateExecutable(t, releaseMisePath(paths), `#!/bin/sh
if [ "$1" = --version ]; then printf '`+testedMiseVersion+`\n'; fi
if [ "$2" = latest ]; then printf '0.6.0\n'; fi
`)
	updater := NewUpdater(paths, io.Discard, "v0.5.0")
	updater.LoadMachine = func() (Machine, error) {
		t.Fatal("old Config read the machine before its release transition")
		return Machine{}, nil
	}
	plan, err := updater.Plan(UpdateSoftware)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Groups) != 2 || plan.Groups[0].State != UpdateAvailable || plan.Groups[1].State != UpdatePending || !plan.HasWork() {
		t.Fatalf("release transition plan = %+v", plan)
	}
}

func TestResumedUpdatePlanChecksTheMachineWithoutRepeatingReleaseDiscovery(t *testing.T) {
	t.Setenv(updateReexecEnv, "v0.6.0")
	updater := Updater{
		Version: "v0.6.0",
		LoadMachine: func() (Machine, error) {
			return Machine{}, nil
		},
	}
	plan, err := updater.Plan(UpdateSoftware)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Groups) != 1 || plan.Groups[0].State != UpdateCurrent || !strings.Contains(plan.Groups[0].Summary, "installed for this update") {
		t.Fatalf("resumed plan = %+v", plan)
	}
}

func (r updatePlanRunner) Run(_ context.Context, _ string, args ...string) Result {
	return r.answers[strings.Join(args, " ")]
}

func (r updatePlanRunner) Exists(string) bool { return r.exists }

func TestUpdatePlanRepairsOnlyAnUnoccupiedGlobalMisePath(t *testing.T) {
	runner := updatePlanRunner{exists: true, answers: map[string]Result{
		"--version": {Stdout: testedMiseVersion},
	}}
	for _, test := range []struct {
		name      string
		occupy    bool
		wantState UpdateState
		wantWork  bool
	}{
		{"missing", false, UpdateAvailable, true},
		{"foreign", true, UpdateUnavailable, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := unboundMisePaths(t)
			if test.occupy {
				if err := os.MkdirAll(miseConfigDir(paths), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(miseConfigDir(paths), "config.toml"), []byte("[tools]\nnode = \"22\"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			updater := Updater{
				Paths: paths, Version: "dev", MachineMiseProbe: runner, MachineMisePlan: runner,
				LoadMachine: func() (Machine, error) { return Machine{Mise: true}, nil },
			}
			plan, err := updater.Plan(UpdateSoftware)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Groups) < 2 || plan.Groups[1].Name != miseName || plan.Groups[1].State != test.wantState {
				t.Fatalf("Mise plan = %+v", plan.Groups)
			}
			if plan.HasWork() != test.wantWork {
				t.Fatalf("HasWork() = %v, want %v for %+v", plan.HasWork(), test.wantWork, plan.Groups)
			}
		})
	}
}

func TestUpdatePlanUsesExactMiseDiscoveryAndHonestDeferredChecks(t *testing.T) {
	paths := testPaths(t)
	runner := updatePlanRunner{exists: true, answers: map[string]Result{
		"--version": {Stdout: testedMiseVersion + "\n"},
		"outdated --json": {Stdout: `{
  "node": {"current":"24.1.0","latest":"24.2.0"},
  "go": {"current":"1.26.0","latest":"1.27.0"}
}`},
		"bootstrap packages status --json": {Stdout: `{"brew":{"packages":[{},{}]},"mas":{"packages":[{}]}}`},
		"bootstrap repos status --json": {Stdout: `{"repos":[
  {"path":"/tmp/current","state":"current"},
  {"path":"/tmp/gizmos","state":"differs"}
]}`},
	}}
	updater := Updater{
		Paths:            paths,
		Version:          "dev",
		MachineMiseProbe: runner,
		MachineMisePlan:  runner,
		LoadMachine: func() (Machine, error) {
			return Machine{
				Mise: true,
				AgentSkills: &AgentSkills{Agents: []string{"codex"}, Sources: []AgentSkillSource{{
					Source: "github.com/example/skills", Skills: []string{"one", "two"},
				}}},
			}, nil
		},
	}
	plan, err := updater.Plan(UpdateAll)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]UpdateGroup{}
	for _, group := range plan.Groups {
		states[group.Name] = group
	}
	if states["Tools"].State != UpdateAvailable || len(states["Tools"].Details) != 2 {
		t.Fatalf("tools = %+v", states["Tools"])
	}
	if states["Packages"].State != UpdatePending || states["Agent skills"].State != UpdatePending {
		t.Fatalf("deferred groups = packages %+v skills %+v", states["Packages"], states["Agent skills"])
	}
	if states["Repositories"].State != UpdateAvailable || !strings.Contains(states["Repositories"].Details[0], "gizmos") {
		t.Fatalf("repositories = %+v", states["Repositories"])
	}
	if !plan.HasWork() {
		t.Fatal("plan with exact and deferred updates reported no work")
	}

	var output bytes.Buffer
	WriteUpdatePlan(&output, plan)
	for _, want := range []string{"Update plan · all", "node 24.1.0 → 24.2.0", "3 declared packages across 2 managers", "2 declared skills"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("preview missing %q:\n%s", want, output.String())
		}
	}
}

func TestRepositoryPlanLabelsUnverifiedRemoteFreshness(t *testing.T) {
	paths := testPaths(t)
	runner := updatePlanRunner{exists: true, answers: map[string]Result{
		"--version":                     {Stdout: testedMiseVersion},
		"bootstrap repos status --json": {Stdout: `{"repos":[{"path":"/tmp/config","state":"current"}]}`},
	}}
	updater := Updater{
		Paths: paths, Version: "dev", MachineMiseProbe: runner, MachineMisePlan: runner,
		LoadMachine: func() (Machine, error) { return Machine{Mise: true}, nil },
	}
	plan, err := updater.Plan(UpdateRepositories)
	if err != nil {
		t.Fatal(err)
	}
	groups := plan.GroupsFor(UpdateRepositories)
	repositories := groups[len(groups)-1]
	if repositories.State != UpdatePending || !strings.Contains(repositories.Summary, "remote freshness is checked when run") {
		t.Fatalf("repositories = %+v", repositories)
	}
}

func TestBlockedUpdatePlanCannotRun(t *testing.T) {
	plan := UpdatePlan{
		Scope: UpdateSoftware, Blocked: true,
		Groups: []UpdateGroup{{Name: "Config", State: UpdateUnavailable}},
	}
	if plan.HasWork() {
		t.Fatal("blocked plan reported runnable work")
	}
	var output bytes.Buffer
	WriteUpdatePlan(&output, plan)
	if !strings.Contains(output.String(), "Update is unavailable") {
		t.Fatalf("blocked preview = %q", output.String())
	}
}

func TestUpdatePlanFingerprintTracksWorkNotCheckTime(t *testing.T) {
	left := UpdatePlan{
		Scope: UpdateSoftware, CheckedAt: time.Unix(1, 0), ResolvedVersion: "v1.2.3",
		Groups: []UpdateGroup{{Name: "Config", Scope: UpdateAll, State: UpdateCurrent, Summary: "v1.2.3 is current"}},
	}
	right := left
	right.CheckedAt = time.Unix(2, 0)
	if left.Fingerprint() != right.Fingerprint() {
		t.Fatal("check time changed update plan identity")
	}
	right.Groups = []UpdateGroup{{Name: "Config", Scope: UpdateAll, State: UpdateAvailable, Summary: "v1.2.3 → v1.2.4"}}
	if left.Fingerprint() == right.Fingerprint() {
		t.Fatal("changed update work kept the same identity")
	}
}

func TestUnavailableChecksAreNotRunnableWork(t *testing.T) {
	plan := UpdatePlan{
		Scope:  UpdateSoftware,
		Groups: []UpdateGroup{{Name: "Tools", Scope: UpdateSoftware, State: UpdateUnavailable, Summary: "network unavailable"}},
	}
	if plan.HasWork() {
		t.Fatal("an unavailable check was presented as runnable work")
	}
	var output bytes.Buffer
	WriteUpdatePlan(&output, plan)
	if !strings.Contains(output.String(), "No runnable updates") || strings.Contains(output.String(), "Everything checked is current") {
		t.Fatalf("unavailable-only preview = %q", output.String())
	}
}

type cancelledUpdatePlanRunner struct{ sawCancellation bool }

func (r *cancelledUpdatePlanRunner) Exists(string) bool { return true }
func (r *cancelledUpdatePlanRunner) Run(ctx context.Context, _ string, _ ...string) Result {
	r.sawCancellation = errors.Is(ctx.Err(), context.Canceled)
	return Result{Err: ctx.Err()}
}

func TestUpdatePlanningCarriesCancellationToProviderCommands(t *testing.T) {
	paths := testPaths(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &cancelledUpdatePlanRunner{}
	updater := Updater{
		Paths: paths, Version: "dev", MachineMiseProbe: runner, MachineMisePlan: runner,
		LoadMachine: func() (Machine, error) { return Machine{Mise: true}, nil },
	}
	if _, err := updater.PlanContext(ctx, UpdateSoftware); err != nil {
		t.Fatal(err)
	}
	if !runner.sawCancellation {
		t.Fatal("provider command did not receive the cancelled planning context")
	}
}
