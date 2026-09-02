package config

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
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

func TestUpdatePlanUsesExactMiseDiscoveryAndHonestDeferredChecks(t *testing.T) {
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
	runner := updatePlanRunner{exists: true, answers: map[string]Result{
		"--version":                     {Stdout: testedMiseVersion},
		"bootstrap repos status --json": {Stdout: `{"repos":[{"path":"/tmp/config","state":"current"}]}`},
	}}
	updater := Updater{
		Version: "dev", MachineMiseProbe: runner, MachineMisePlan: runner,
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
