package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type pruneStubRunner struct{ currentRestore string }

func (r *pruneStubRunner) Run(_ context.Context, name string, args ...string) Result {
	if name == "git" && strings.Join(args, " ") == "config --local --get "+restoreCheckoutKey {
		return Result{Stdout: r.currentRestore}
	}
	return Result{Err: fmt.Errorf("unexpected probe: %s %v", name, args)}
}
func (*pruneStubRunner) Exists(string) bool { return true }

type pruneFixture struct{ pruner Pruner }

func newPruneFixture(t *testing.T) pruneFixture {
	t.Helper()
	paths := testPaths(t)
	return pruneFixture{Pruner{Paths: paths, Machine: Machine{}, Runner: &pruneStubRunner{}, Log: Logger{Out: io.Discard}}}
}

func TestPrunePreservesNativeResourcesAndRechecksConfigFiles(t *testing.T) {
	fixture := newPruneFixture(t)
	p := fixture.pruner
	// Retired resource ledgers and native files are outside this plan's authority.
	foreign := p.Paths.InRoot("native-owned")
	if err := os.WriteFile(foreign, []byte("native"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := (Baselines{Dir: p.Paths.StateDir}).Save(chromePWAsID, json.RawMessage(`[]`)); err != nil {
		t.Fatal(err)
	}
	plan, err := p.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.files) != 1 {
		t.Fatalf("plan: %+v", plan)
	}
	var out bytes.Buffer
	WritePrunePlan(&out, plan)
	if !strings.Contains(out.String(), chromePWAsName) {
		t.Fatalf("preview: %s", out.String())
	}
	if err := (Baselines{Dir: p.Paths.StateDir}).Save(chromePWAsID, json.RawMessage(`[{}]`)); err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(plan); err == nil {
		t.Fatal("changed plan was applied")
	}
	fresh, err := p.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatal("native file changed:", err)
	}
}

func TestPruneRefusesEveryFileThatChangedAfterPreview(t *testing.T) {
	// ARCHITECTURE promises Config revalidates every file it removes
	// immediately before the operation. These are the two functions that
	// actually delete, and nothing drove either of them.
	dir := t.TempDir()
	path := filepath.Join(dir, "baseline.json")
	body := []byte("{\"schema\":1}\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	file := pruneFile{Label: "a baseline", Path: path, Digest: contentDigest(body)}

	if err := os.WriteFile(path, []byte("{\"schema\":2}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyPruneFile(file); err == nil {
		t.Fatal("a file that changed after preview was deleted")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the refused file was removed anyway: %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", path); err != nil {
		t.Fatal(err)
	}
	if err := applyPruneFile(file); err == nil {
		t.Fatal("a path that is no longer a regular file was followed")
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("the refused symlink was removed: %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyPruneFile(file); err != nil {
		t.Fatalf("an unchanged file was refused: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("an unchanged file survived the prune")
	}
}

func TestPruneReclaimsOnlyTheMarkersNothingWillActOn(t *testing.T) {
	// A marker records a step a killed run still owes. One the document no
	// longer declares is a step nothing will ever take, and nothing else
	// removes it.
	fixture := newPruneFixture(t)
	pruner := fixture.pruner
	pruner.Machine.Preferences = []PreferenceBackup{{
		ID: "example-app", Name: "Example App",
		Bundle: "com.example.ExampleApp", Domain: "com.example.ExampleApp",
	}}
	for _, name := range []string{
		"dock-restart",
		relaunchMarker("com.example.ExampleApp"),
		relaunchMarker("com.example.Retired"),
	} {
		if err := setMarker(pruner.Paths, name); err != nil {
			t.Fatal(err)
		}
	}
	planned, warnings, err := pruner.planPendingMarkers()
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(planned) != 2 {
		t.Fatalf("planned = %+v, want the retired Dock restart and undeclared relaunch", planned)
	}
	for _, file := range planned {
		if err := applyPruneFile(file); err != nil {
			t.Fatal(err)
		}
	}
	if markerSet(pruner.Paths, "dock-restart") {
		t.Fatal("retired Dock marker survived prune")
	}
	if markerSet(pruner.Paths, relaunchMarker("com.example.Retired")) {
		t.Fatal("the undeclared marker survived the prune")
	}
	for _, kept := range []string{relaunchMarker("com.example.ExampleApp")} {
		if !markerSet(pruner.Paths, kept) {
			t.Fatalf("prune removed %s, which the machine still declares", kept)
		}
	}

	// A file Config did not write is left alone.
	stray := filepath.Join(pruner.Paths.StateDir, "pending", "relaunch-com.example.Stray")
	if err := os.WriteFile(stray, []byte("not a marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	planned, warnings, err = pruner.planPendingMarkers()
	if err != nil {
		t.Fatal(err)
	}
	if len(planned) != 0 {
		t.Fatalf("an unrecognised file was planned for deletion: %+v", planned)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unrecognised") {
		t.Fatalf("warnings = %v", warnings)
	}
}
