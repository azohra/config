package config

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pendingRestoreFixture(t *testing.T) (Paths, Machine) {
	t.Helper()
	paths := managedTestPaths(t)
	source := filepath.Join(t.TempDir(), "machine.git")
	source = repositoryFixtureAt(t, source, machineDocumentForRepository(source))
	machine, pending, err := MaterializeRepository(paths, source, io.Discard, io.Discard)
	if err != nil || !pending {
		t.Fatalf("materialize restore fixture = pending %t, %v", pending, err)
	}
	return paths, machine
}

func TestPendingRestoreRefusesChangedCheckoutState(t *testing.T) {
	t.Run("local changes", func(t *testing.T) {
		paths, machine := pendingRestoreFixture(t)
		if err := os.WriteFile(paths.InRoot("local-change"), []byte("changed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := pendingRestore(paths, machine, io.Discard); err == nil || !strings.Contains(err.Error(), "local changes") {
			t.Fatalf("pending restore with local changes = %v", err)
		}
	})

	t.Run("commit", func(t *testing.T) {
		paths, machine := pendingRestoreFixture(t)
		gitTest(t, paths.Root, "config", "user.name", "Config Test")
		gitTest(t, paths.Root, "config", "user.email", "config@example.invalid")
		if err := os.WriteFile(paths.InRoot("tracked-change"), []byte("changed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitTest(t, paths.Root, "add", "tracked-change")
		gitTest(t, paths.Root, "commit", "--quiet", "-m", "Change pending restore")
		// HEAD moving is what a snapshot save does, and the record is bound to
		// the commit it was cloned at. Refusing forever left every later
		// bootstrap failing for a restore nothing could finish.
		var said strings.Builder
		progress, pending, err := pendingRestore(paths, machine, &said)
		if err != nil || pending {
			t.Fatalf("pending restore after another commit = pending %t, %v", pending, err)
		}
		if !strings.Contains(said.String(), "abandoned the pending bootstrap restore") {
			t.Fatalf("the abandonment was not reported: %q", said.String())
		}
		identifier, found, idErr := checkoutRestoreID(paths)
		if idErr != nil || !found {
			t.Fatalf("restore identity = found %t, %v", found, idErr)
		}
		if _, statErr := os.Stat(restoreStatePath(paths, identifier)); !os.IsNotExist(statErr) {
			t.Fatalf("the abandoned record survived: %v", statErr)
		}
		_ = progress
		// A second bootstrap on the same checkout now starts clean.
		if _, pending, err = pendingRestore(paths, machine, io.Discard); err != nil || pending {
			t.Fatalf("a later bootstrap still saw a pending restore: pending %t, %v", pending, err)
		}
	})

	t.Run("plan", func(t *testing.T) {
		paths, machine := pendingRestoreFixture(t)
		identifier, found, err := checkoutRestoreID(paths)
		if err != nil || !found {
			t.Fatalf("restore identity = found %t, %v", found, err)
		}
		statePath := restoreStatePath(paths, identifier)
		data, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatal(err)
		}
		var record restoreRecord
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatal(err)
		}
		record.Plan = "sha256:" + strings.Repeat("0", 64)
		data, err = json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := AtomicWrite(statePath, append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := pendingRestore(paths, machine, io.Discard); err == nil || !strings.Contains(err.Error(), "restore plan changed") {
			t.Fatalf("pending restore with another plan = %v", err)
		}
	})
}

func TestASavedSnapshotDoesNotBlockEveryLaterBootstrap(t *testing.T) {
	// The record binds resumption to the commit cloned at bootstrap, and
	// Config's own Save moves HEAD. Nothing else consulted the record, so a
	// capture and save between two bootstrap runs refused every later one.
	paths, machine := pendingRestoreFixture(t)
	gitTest(t, paths.Root, "config", "user.name", "Config Test")
	gitTest(t, paths.Root, "config", "user.email", "config@example.invalid")
	if err := os.WriteFile(paths.InRoot("snapshots", "captured"), []byte("state\n"), 0o600); err != nil {
		if err := os.MkdirAll(paths.InRoot("snapshots"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(paths.InRoot("snapshots", "captured"), []byte("state\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitTest(t, paths.Root, "add", "-A")
	gitTest(t, paths.Root, "commit", "--quiet", "-m", "Update machine snapshot")

	var said strings.Builder
	_, pending, err := pendingRestore(paths, machine, &said)
	if err != nil {
		t.Fatalf("a saved snapshot left bootstrap refusing: %v", err)
	}
	if pending {
		t.Fatal("a restore bound to an older commit was reported resumable")
	}
	if !strings.Contains(said.String(), "abandoned") {
		t.Fatalf("the abandonment was not reported: %q", said.String())
	}
}
