package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
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

type nativePruneRunner struct {
	nativeStatusRunner
	preview string
}

func (r *nativePruneRunner) Run(ctx context.Context, name string, args ...string) Result {
	if slices.Equal(args, []string{"bootstrap", "packages", "status", "--json"}) {
		return Result{Stdout: `{"brew":{"available":true,"packages":[{"package":"example"}]}}`}
	}
	if slices.Contains(args, "prune") {
		return Result{Stdout: r.preview}
	}
	return r.nativeStatusRunner.Run(ctx, name, args...)
}

func TestNativeCleanupRefusesAChangedPreviewAndRunsOnlyConfirmedScopes(t *testing.T) {
	native, _ := nativeFixture(t)
	gitTest(t, native.Paths.Root, "init", "--quiet")
	runner := &nativePruneRunner{preview: "would remove unused example@1\n"}
	native.Probe = runner
	log := filepath.Join(t.TempDir(), "commands")
	t.Setenv("NATIVE_PRUNE_TEST_LOG", log)
	writeUpdateExecutable(t, native.Paths.InHome(".local", "bin", "mise"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$NATIVE_PRUNE_TEST_LOG\"\n")
	p := newPruneFixture(t).pruner
	p.Paths, p.Mise = native.Paths, &native
	plan, err := p.Plan()
	if err != nil || len(plan.native) != 2 {
		t.Fatalf("native cleanup preview = %+v, %v", plan, err)
	}
	runner.preview = "would remove unused example@2\n"
	if err := p.Apply(plan); err == nil || !strings.Contains(err.Error(), "plan changed") {
		t.Fatalf("changed native cleanup was accepted: %v", err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("changed cleanup executed a command")
	}
	fresh, err := p.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Fingerprint() == plan.Fingerprint() {
		t.Fatal("cleanup confirmation did not bind the native preview")
	}
	if err := p.Apply(fresh); err != nil {
		t.Fatal(err)
	}
	commands, _ := os.ReadFile(log)
	if string(commands) != "prune --yes\nbootstrap packages prune --manager brew --yes\n" {
		t.Fatalf("native cleanup scope = %s", commands)
	}
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

type nativeManagersRunner struct {
	nativeStatusRunner
	previews map[string]Result
}

func (r *nativeManagersRunner) Run(ctx context.Context, name string, args ...string) Result {
	if slices.Equal(args, []string{"bootstrap", "packages", "status", "--json"}) {
		return Result{Stdout: `{
		  "brew-cask":{"available":true,"packages":[{"package":"example"}]},
		  "mas":{"available":true,"packages":[{"package":"123"}]}
		}`}
	}
	if result, ok := r.previews[strings.Join(args, " ")]; ok {
		return result
	}
	return r.nativeStatusRunner.Run(ctx, name, args...)
}

func TestNativeCleanupBindsActionsAndReportsWhatItLeaves(t *testing.T) {
	native, _ := nativeFixture(t)
	gitTest(t, native.Paths.Root, "init", "--quiet")
	// The shape of mise 2026.10.21 dry runs: actions and notes share stderr.
	tools := "mise pruned configuration links [dryrun]\n" +
		"mise scanning tracked configs          ✓ done\n" +
		"mise node@22.20.0 is kept: still running as pid 5137 (node) and 14 more\n" +
		"mise cmake@4.4.4 is prunable: no tracked config or tool stub requires cmake\n" +
		"mise cmake@4.4.4 [dryrun]                uninstall\n"
	runner := &nativeManagersRunner{previews: map[string]Result{
		"prune --yes --dry-run": {Stderr: tools},
		"bootstrap packages prune --manager brew-cask --yes --dry-run": {Stderr: "mise WARN  brew-cask:craft: skipped: metadata-only app ownership cannot be proven safely during pruning\n" +
			"mise brew-cask: to remove craft anyway, run again with --force\n" +
			"mise brew-cask: nothing to prune\n"},
		"bootstrap packages prune --manager mas --yes --dry-run": {Err: errors.New("exit status 1"), Stderr: "mise ERROR package manager 'mas' does not support pruning\n"},
	}}
	native.Probe = runner
	p := newPruneFixture(t).pruner
	p.Paths, p.Mise = native.Paths, &native
	plan, err := p.Plan()
	if err != nil || len(plan.native) != 1 || plan.native[0].Operation.Args[0] != "prune" {
		t.Fatalf("native cleanup plan = %+v, %v", plan, err)
	}
	if !slices.Equal(plan.warnings, []string{"brew-cask packages: brew-cask:craft: skipped: metadata-only app ownership cannot be proven safely during pruning"}) {
		t.Fatalf("cleanup warnings = %q", plan.warnings)
	}
	var preview strings.Builder
	WritePrunePlan(&preview, plan)
	if !strings.Contains(preview.String(), "is kept: still running") || strings.Contains(preview.String(), "does not support pruning") {
		t.Fatalf("cleanup preview:\n%s", preview.String())
	}

	runner.previews["prune --yes --dry-run"] = Result{Stderr: strings.Replace(tools, "pid 5137 (node) and 14 more", "pid 6001 (node) and 15 more", 1)}
	again, err := p.Plan()
	if err != nil || !plan.sameWork(again) || again.Fingerprint() != plan.Fingerprint() {
		t.Fatalf("a process change invalidated the confirmed cleanup: %v", err)
	}
	runner.previews["prune --yes --dry-run"] = Result{Stderr: strings.ReplaceAll(tools, "cmake@4.4.4", "cmake@4.4.5")}
	if changed, _ := p.Plan(); changed.Fingerprint() == plan.Fingerprint() {
		t.Fatal("a different removal kept the confirmed cleanup")
	}
	runner.previews["prune --yes --dry-run"] = Result{Stderr: "mise pruned configuration links [dryrun]\nmise node@22.20.0 is kept: still running as pid 5137 (node)\n"}
	if idle, err := p.Plan(); err != nil || !idle.Empty() {
		t.Fatalf("cleanup with nothing to remove = %+v, %v", idle, err)
	}
}
