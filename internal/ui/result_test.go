package ui

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/azohra/config/internal/config"
)

func TestOperationResultRoundTripsThroughPrivateConfigState(t *testing.T) {
	paths := config.Paths{StateDir: filepath.Join(t.TempDir(), "state")}
	log := newOperationLog()
	log.Append(config.OperationEvent{Kind: config.OperationSection, Text: "Config"})
	log.Append(config.OperationEvent{Kind: config.OperationOK, Text: "current"})
	log.Append(config.OperationEvent{Kind: config.OperationOutput, Text: "provider detail\n"})
	log.progress.omittedLines = 2
	log.diagnostics.omittedLines = 3
	want := operationResult{
		label: "Software update", log: log, err: errors.New("packages failed"),
		finishedAt: time.Date(2026, 9, 2, 15, 4, 5, 0, time.UTC),
		duration:   3*time.Minute + 2*time.Second,
	}
	if err := saveOperationResult(paths, want); err != nil {
		t.Fatal(err)
	}
	got := loadOperationResult(paths)
	if got.label != want.label || got.log.progress.String() != want.log.progress.String() ||
		got.log.diagnostics.String() != want.log.diagnostics.String() || got.err == nil || got.err.Error() != want.err.Error() ||
		!got.finishedAt.Equal(want.finishedAt) || got.duration != want.duration ||
		got.log.progress.omittedLines != want.log.progress.omittedLines || got.log.diagnostics.omittedLines != want.log.diagnostics.omittedLines {
		t.Fatalf("loaded result = %+v, want %+v", got, want)
	}
	if len(got.log.progress.spans) < 2 || got.log.progress.spans[1].kind != config.OperationOK {
		t.Fatalf("loaded result lost typed progress: %+v", got.log.progress.spans)
	}
	info, err := os.Stat(operationResultPath(paths))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("result permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestLegacyOperationResultRemainsReadable(t *testing.T) {
	paths := config.Paths{StateDir: filepath.Join(t.TempDir(), "state")}
	if err := os.MkdirAll(paths.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"schema":1,"label":"Apply","output":"Config\n  ✓ current\n","finished_at":"2026-09-02T15:04:05Z"}`
	if err := os.WriteFile(operationResultPath(paths), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadOperationResult(paths)
	if got.label != "Apply" || got.log.diagnostics.String() != "Config\n  ✓ current\n" || got.log.progress.String() != "" {
		t.Fatalf("legacy result = %+v", got)
	}
}

func TestSchemaTwoResultPreservesItsRenderedTranscriptAsDiagnostics(t *testing.T) {
	paths := config.Paths{StateDir: filepath.Join(t.TempDir(), "state")}
	if err := os.MkdirAll(paths.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stored := `{"schema":2,"label":"Update","spans":[{"kind":"output","text":"raw detail\n  "},{"kind":"ok","text":"✓"},{"kind":"output","text":" current\n"}],"finished_at":"2026-09-02T15:04:05Z"}`
	if err := os.WriteFile(operationResultPath(paths), []byte(stored), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadOperationResult(paths)
	if got.log.diagnostics.String() != "raw detail\n  ✓ current\n" || got.log.progress.String() != "" {
		t.Fatalf("migrated result = %+v", got.log)
	}
}

func TestUnreadableOperationResultIsIgnored(t *testing.T) {
	paths := config.Paths{StateDir: filepath.Join(t.TempDir(), "state")}
	if err := os.MkdirAll(paths.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(operationResultPath(paths), []byte(`{"schema":99,"label":"stale"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadOperationResult(paths); got.label != "" {
		t.Fatalf("unsupported result loaded as %+v", got)
	}
}

func writeUpdateRun(t *testing.T, paths config.Paths, run config.UpdateRun) {
	t.Helper()
	data, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(paths.StateDir, "updates")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	name := run.StartedAt.UTC().Format("20060102T150405.000Z.json")
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLastResultIsALaterCommandLineUpdate(t *testing.T) {
	paths := config.Paths{StateDir: filepath.Join(t.TempDir(), "state")}
	saved := time.Date(2026, 11, 11, 20, 30, 0, 0, time.UTC)
	if err := saveOperationResult(paths, operationResult{label: "Save", log: newOperationLog(), finishedAt: saved}); err != nil {
		t.Fatal(err)
	}
	run := config.UpdateRun{
		Schema: 1, Scope: "tools", StartedAt: saved.Add(-2 * time.Hour), FinishedAt: saved.Add(-time.Hour),
		Steps: []config.StepResult{{ID: "tools", Name: "Tools", Status: config.StepFailed, Error: "registry refused", Items: []config.ItemResult{
			{Name: "node", From: "24.22.2", To: "24.22.3", Status: config.ItemUpdated},
			{Name: "vercel", From: "65.5.0", To: "65.14.0", Status: config.ItemFailed},
		}}},
	}
	writeUpdateRun(t, paths, run)
	if got := loadLastResult(paths); got.label != "Save" {
		t.Fatalf("an earlier update replaced the stored result: %q", got.label)
	}

	run.StartedAt, run.FinishedAt = saved.Add(time.Hour), saved.Add(time.Hour+90*time.Second)
	writeUpdateRun(t, paths, run)
	got := loadLastResult(paths)
	progress := got.log.progress.String()
	if got.label != "Update tools" || !got.fromHistory || !got.finishedAt.Equal(run.FinishedAt) || got.duration != 90*time.Second || got.err == nil {
		t.Fatalf("last result = %+v", got)
	}
	for _, want := range []string{"node 24.22.2 → 24.22.3", "vercel 65.5.0 → 65.14.0: failed", "registry refused"} {
		if !strings.Contains(progress, want) {
			t.Fatalf("progress lacks %q:\n%s", want, progress)
		}
	}
}

func TestRefreshingShowsOnlyThisSessionsOperation(t *testing.T) {
	log := newOperationLog()
	log.Append(config.OperationEvent{Kind: config.OperationOK, Text: "pushed to origin/main"})
	m := Model{loading: true, width: 80, height: 24, last: operationResult{label: "Save", log: log, fromHistory: true}}
	if view := m.renderInspecting(); strings.Contains(view, "pushed to origin/main") || !strings.Contains(view, "Inspecting this Mac") {
		t.Fatalf("a stored result was shown while inspecting:\n%s", view)
	}
	m.last.fromHistory = false
	if view := m.renderInspecting(); !strings.Contains(view, "pushed to origin/main") || !strings.Contains(view, "Refreshing status") {
		t.Fatalf("this session's result was not shown while refreshing:\n%s", view)
	}
}
