package config

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"howett.net/plist"
)

type preferenceRunner struct {
	exported []byte
}

func (r preferenceRunner) Run(_ context.Context, name string, args ...string) Result {
	if name == "defaults" && slices.Equal(args, []string{"export", testMachine().Preferences[0].Domain, "-"}) {
		return Result{Stdout: string(r.exported)}
	}
	return Result{Err: errors.New("unexpected command")}
}

func (preferenceRunner) Exists(string) bool { return false }

func TestPreferenceInspectionOnlyRequiresAValidBackup(t *testing.T) {
	paths := testPaths(t)
	preference := testMachine().Preferences[0]
	missing := preference.Inspect(paths)
	if missing.State != Uncaptured || missing.Failed() != 0 || missing.Bidirectional || !slices.Equal(missing.Actions, []Action{Capture}) {
		t.Fatalf("missing backup = %#v", missing)
	}

	data, err := plist.Marshal(map[string]any{"layout": "current"}, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(preference.snapshotPath(paths), data, 0o600); err != nil {
		t.Fatal(err)
	}
	current := preference.Inspect(paths)
	if current.State != Current || len(current.Actions) != 0 || current.Summary != "preference backup available" {
		t.Fatalf("valid backup = %#v", current)
	}
}

func TestPreferenceBackupCopiesTheCompleteLiveDomain(t *testing.T) {
	paths := testPaths(t)
	preference := testMachine().Preferences[0]
	exported, err := plist.Marshal(map[string]any{
		"layout":          "latest",
		"firstRun":        true,
		"SULastCheckTime": "today",
		"license":         []byte(`{"key":"owned-by-the-app"}`),
	}, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	if err := preference.Backup(paths, preferenceRunner{exported: exported}); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(preference.snapshotPath(paths))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(saved, exported) {
		t.Fatal("backup filtered or rewrote the live plist")
	}
	info, err := os.Stat(preference.snapshotPath(paths))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %o, want 600", info.Mode().Perm())
	}
}
