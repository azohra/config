package config

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"strings"
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

func TestPreferenceBackupRefusesADomainThatHoldsNothing(t *testing.T) {
	// defaults exits 0 for a domain that does not exist and prints <dict/>.
	// Capturing that would report a backup over settings never read.
	paths := testPaths(t)
	preference := testMachine().Preferences[0]
	empty, err := plist.Marshal(map[string]any{}, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	err = preference.Backup(paths, preferenceRunner{exported: empty})
	if err == nil {
		t.Fatal("an empty defaults domain was captured as a backup")
	}
	if !strings.Contains(err.Error(), preference.Domain) {
		t.Fatalf("refusal does not name the domain: %v", err)
	}
	if _, statErr := os.Stat(preference.snapshotPath(paths)); !os.IsNotExist(statErr) {
		t.Fatal("refused capture still wrote a snapshot")
	}

	// An artifact already committed with nothing in it reports the way a
	// missing one does, so Capture stays on offer.
	if err := atomicWrite(preference.snapshotPath(paths), empty, 0o600); err != nil {
		t.Fatal(err)
	}
	resource := preference.Inspect(paths)
	if resource.State != Uncaptured || !slices.Equal(resource.Actions, []Action{Capture}) {
		t.Fatalf("empty saved domain = %#v", resource)
	}
}

func TestPreferenceThatCannotBeReadCanStillBeRecaptured(t *testing.T) {
	// Without an action the resource fails preflight forever and the product
	// offers no way to replace the artifact it cannot read.
	paths := testPaths(t)
	preference := testMachine().Preferences[0]
	if err := atomicWrite(preference.snapshotPath(paths), []byte("not a property list"), 0o600); err != nil {
		t.Fatal(err)
	}
	resource := preference.Inspect(paths)
	if resource.State != Unavailable || resource.Failed() == 0 {
		t.Fatalf("corrupt backup = %#v", resource)
	}
	if !resource.Allows(Capture) {
		t.Fatalf("no way to replace an unreadable backup: %#v", resource.Actions)
	}
}
