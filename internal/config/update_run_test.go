package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUpdateRecordsKeepTheNewestRunsAndExplainAFailure(t *testing.T) {
	paths := testPaths(t)
	if _, err := LastUpdateRun(paths); err == nil {
		t.Fatal("a Mac with no update reported one")
	}
	start := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	for index := range keptUpdateRuns + 3 {
		run := UpdateRun{Schema: updateRunSchema, Scope: "software", StartedAt: start.Add(time.Duration(index) * time.Minute)}
		if err := run.save(paths); err != nil {
			t.Fatal(err)
		}
	}
	if names := updateRunNames(updateRunDir(paths)); len(names) != keptUpdateRuns {
		t.Fatalf("kept %d records, want %d", len(names), keptUpdateRuns)
	}
	failed := UpdateRun{
		Schema: updateRunSchema, Scope: "software", StartedAt: start.Add(time.Hour), FinishedAt: start.Add(time.Hour + 90*time.Second),
		Steps: []StepResult{{
			ID: toolsStepID, Name: "Tools", Status: StepFailed, Command: "mise upgrade --global --no-prune --yes",
			Error: "example-cli: no release asset", Output: "first line\nmise ERROR example-cli: no release asset", Duration: 41 * time.Second,
			Items: []ItemResult{
				{UpdateItem{Name: "node", From: "24.1.0", To: "24.2.0"}, ItemUpdated},
				{UpdateItem{Name: "example-cli", From: "1.0.0", To: "1.1.0"}, ItemFailed},
			},
		}, {ID: filesStepID, Name: "Skills, MCP and dotfiles", Status: StepSkipped, Error: "the repository update failed"}},
	}
	if err := failed.save(paths); err != nil {
		t.Fatal(err)
	}
	last, err := LastUpdateRun(paths)
	if err != nil || !last.Failed() || !last.StartedAt.Equal(failed.StartedAt) {
		t.Fatalf("last record = %+v, %v", last, err)
	}
	var report bytes.Buffer
	WriteUpdateRun(&report, last)
	for _, want := range []string{
		"Update software", "1m30s", "Tools: 1 of 2 updated · 41s", "node 24.1.0 → 24.2.0",
		"example-cli 1.0.0 → 1.1.0: failed", "error: example-cli: no release asset",
		"command: mise upgrade --global --no-prune --yes", "mise ERROR example-cli", "Skills, MCP and dotfiles: skipped",
	} {
		if !strings.Contains(report.String(), want) {
			t.Fatalf("report is missing %q:\n%s", want, report.String())
		}
	}
}

func TestAFailedRepositoryUpdateHoldsLinkedFilesAndIsRecorded(t *testing.T) {
	native, runner := nativeFixture(t)
	machine := "kind = \"azohra.config.machine\"\nschema = 8\n[repository]\nbranch = \"main\"\nurl = \"https://example.com/machine.git\"\n"
	if err := os.WriteFile(native.Paths.InRoot("config.toml"), []byte(machine), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, native.Paths.Root, "init", "--quiet")
	log := filepath.Join(t.TempDir(), "commands")
	t.Setenv("NATIVE_MISE_TEST_LOG", log)
	writeUpdateExecutable(t, native.Paths.InHome(".local", "bin", "mise"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$NATIVE_MISE_TEST_LOG\"\necho 'mise ERROR ~/projects/example: not a fast-forward' >&2\nexit 1\n")
	var output bytes.Buffer
	native.Live.Stdout, native.Live.Stderr = &output, &output
	runner.replies = map[string]Result{
		"bootstrap repos status --json": {Stdout: `{"repos":[{"path_raw":"~/projects/example","ref":"main","current_ref":"main","state":"differs"}]}`},
		"dotfiles status --json":        {Stdout: `{"files":[{"target":"~/.example","state":"missing"}]}`},
	}
	updater := NewUpdater(native.Paths, &output, "dev")
	updater.Native = &native
	plan, err := updater.Plan(UpdateRepositories)
	if err != nil || !plan.HasWork() {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	err = updater.Apply(plan)
	if err == nil || !strings.Contains(err.Error(), "Repositories: ~/projects/example: not a fast-forward") {
		t.Fatalf("apply = %v", err)
	}
	if commands, _ := os.ReadFile(log); !strings.HasPrefix(string(commands), "bootstrap repos update --skip-dirty --yes\n") || strings.Contains(string(commands), "dotfiles apply") {
		t.Fatalf("linked files were applied over a failed repository update: %s", commands)
	}
	// A plan whose declarations changed stops before any step, and says so.
	if err := os.WriteFile(native.Paths.InRoot("mise", "added.toml"), []byte("# new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := updater.Apply(plan); err == nil || !strings.Contains(err.Error(), "configuration changed") {
		t.Fatalf("changed declarations = %v", err)
	}
	if stopped, _ := LastUpdateRun(native.Paths); len(stopped.Steps) != 0 || !stopped.Failed() || !strings.Contains(stopped.Error, "configuration changed") {
		t.Fatalf("a stopped update left no reason: %+v", stopped)
	}
	if err := os.Remove(native.Paths.InRoot("mise", "added.toml")); err != nil {
		t.Fatal(err)
	}
	if err := updater.Apply(plan); err == nil {
		t.Fatal("the failing repository update succeeded")
	}
	run, err := LastUpdateRun(native.Paths)
	if err != nil || len(run.Steps) != 2 || run.Steps[0].Status != StepFailed || run.Steps[0].Items[0].Status != ItemFailed || run.Steps[1].Status != StepSkipped {
		t.Fatalf("record = %+v, %v", run, err)
	}
}
