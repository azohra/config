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
	var output terminalOutput
	output.Append(config.OperationEvent{Kind: config.OperationSection, Text: "Config"})
	output.Append(config.OperationEvent{Kind: config.OperationOK, Text: "current"})
	want := operationResult{
		label: "Software update", output: output, err: errors.New("packages failed"),
		finishedAt: time.Date(2026, 9, 2, 15, 4, 5, 0, time.UTC),
	}
	if err := saveOperationResult(paths, want); err != nil {
		t.Fatal(err)
	}
	got := loadOperationResult(paths)
	if got.label != want.label || got.output.String() != want.output.String() || got.err == nil || got.err.Error() != want.err.Error() || !got.finishedAt.Equal(want.finishedAt) {
		t.Fatalf("loaded result = %+v, want %+v", got, want)
	}
	if len(got.output.spans) < 2 || got.output.spans[1].kind != config.OperationOK {
		t.Fatalf("loaded result lost typed progress: %+v", got.output.spans)
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
	if got.label != "Apply" || got.output.String() != "Config\n  ✓ current\n" {
		t.Fatalf("legacy result = %+v", got)
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
