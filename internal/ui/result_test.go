package ui

import (
	"errors"
	"os"
	"path/filepath"
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
