package config

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestUpdatePlanDiscoversOnlyConfigReleases(t *testing.T) {
	paths := testPaths(t)
	writeUpdateExecutable(t, releaseMisePath(paths), `#!/bin/sh
if [ "$1" = --version ]; then printf '`+releaseMiseVersion+`\n'; fi
if [ "$2" = latest ]; then printf '0.6.0\n'; fi
`)
	updater := NewUpdater(paths, io.Discard, "v0.5.0")

	plan, err := updater.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Groups) != 1 || plan.Groups[0].State != UpdateAvailable || !plan.HasWork() {
		t.Fatalf("release transition plan = %+v", plan)
	}
}

func TestResumedUpdatePlanDoesNotRepeatReleaseDiscovery(t *testing.T) {
	t.Setenv(UpdateReexecEnv, "v0.6.0")
	paths := testPaths(t)
	canonical := configCommandPath(paths)
	writeUpdateExecutable(t, canonical, "#!/bin/sh\nexit 0\n")
	updater := Updater{
		Version: "v0.6.0", Config: canonical,
		CurrentExecutable: func() (string, error) { return canonical, nil },
	}
	plan, err := updater.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Groups) != 1 || plan.Groups[0].State != UpdateCurrent || !strings.Contains(plan.Groups[0].Summary, "installed for this update") {
		t.Fatalf("resumed plan = %+v", plan)
	}
}

func TestBlockedUpdatePlanCannotRun(t *testing.T) {
	plan := UpdatePlan{
		Blocked: true,
		Groups:  []UpdateGroup{{Name: "Config", State: UpdateUnavailable}},
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
		CheckedAt: time.Unix(1, 0), ResolvedVersion: "v1.2.3",
		Groups: []UpdateGroup{{Name: "Config", State: UpdateCurrent, Summary: "v1.2.3 is current"}},
	}
	right := left
	right.CheckedAt = time.Unix(2, 0)
	if left.Fingerprint() != right.Fingerprint() {
		t.Fatal("check time changed update plan identity")
	}
	right.Groups = []UpdateGroup{{Name: "Config", State: UpdateAvailable, Summary: "v1.2.3 → v1.2.4"}}
	if left.Fingerprint() == right.Fingerprint() {
		t.Fatal("changed update work kept the same identity")
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
	updater := Updater{Paths: paths, Version: "v0.5.0", ReleaseMiseProbe: runner}
	if _, err := updater.PlanContext(ctx); err != nil {
		t.Fatal(err)
	}
	if !runner.sawCancellation {
		t.Fatal("provider command did not receive the cancelled planning context")
	}
}
