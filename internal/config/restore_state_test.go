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
		if _, _, err := pendingRestore(paths, machine); err == nil || !strings.Contains(err.Error(), "local changes") {
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
		if _, _, err := pendingRestore(paths, machine); err == nil || !strings.Contains(err.Error(), "checkout changed") {
			t.Fatalf("pending restore after another commit = %v", err)
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
		if err := atomicWrite(statePath, append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := pendingRestore(paths, machine); err == nil || !strings.Contains(err.Error(), "restore plan changed") {
			t.Fatalf("pending restore with another plan = %v", err)
		}
	})
}
