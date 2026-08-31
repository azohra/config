package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type pruneStubRunner struct {
	root               string
	currentRestore     string
	prunableTools      string
	packageStatus      string
	packageDryRuns     map[string]Result
	prunableToolStderr string
}

func (r *pruneStubRunner) Run(_ context.Context, name string, args ...string) Result {
	switch {
	case name == "mise" && slices.Equal(args, []string{"--version"}):
		return Result{Stdout: testedMiseVersion}
	case name == "mise" && slices.Equal(args, []string{"ls", "--prunable", "-J"}):
		return Result{Stdout: r.prunableTools, Stderr: r.prunableToolStderr}
	case name == "mise" && slices.Equal(args, []string{"bootstrap", "packages", "status", "--json"}):
		return Result{Stdout: r.packageStatus}
	case name == "mise" && len(args) == 6 && slices.Equal(args[:4], []string{"bootstrap", "packages", "prune", "--manager"}) && args[5] == "--dry-run":
		return r.packageDryRuns[args[4]]
	case name == "mise" && slices.Equal(args, []string{"config", "ls", "-J"}):
		return Result{Stdout: "[]"}
	case name == "git" && slices.Equal(args, []string{"config", "--local", "--get", restoreCheckoutKey}):
		return Result{Stdout: r.currentRestore}
	case name == "git" && len(args) == 5 && args[0] == "-C" && args[1] == r.root &&
		args[2] == "rev-parse" && args[3] == "--path-format=absolute" && args[4] == "--git-common-dir":
		return Result{Stdout: filepath.Join(r.root, ".git")}
	case name == "git" && len(args) == 6 && args[0] == "-C" && args[1] == r.root &&
		args[2] == "rev-parse" && args[3] == "--path-format=absolute" && args[4] == "--git-path" && args[5] == "hooks":
		return Result{Stdout: filepath.Join(r.root, ".git", "hooks")}
	default:
		return Result{Err: fmt.Errorf("unexpected command: %s %v", name, args)}
	}
}

func (*pruneStubRunner) Exists(string) bool { return true }

type pruneCommandRecorder struct {
	commands []string
}

func (r *pruneCommandRecorder) Command(name string, args ...string) error {
	r.commands = append(r.commands, strings.Join(append([]string{name}, args...), " "))
	return nil
}

type pruneFixture struct {
	pruner         Pruner
	live           *pruneCommandRecorder
	dockBaseline   string
	chromeBaseline string
	oldRestore     string
	currentRestore string
	hook           string
	manifest       string
}

func newPruneFixture(t *testing.T) pruneFixture {
	t.Helper()
	paths := testPaths(t)
	hooks := filepath.Join(paths.Root, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}

	miseState := t.TempDir()
	for _, dir := range []string{"tracked-configs", "trusted-configs"} {
		if err := os.MkdirAll(filepath.Join(miseState, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		missing := filepath.Join(t.TempDir(), "gone.toml")
		if err := os.Symlink(missing, filepath.Join(miseState, dir, "dead")); err != nil {
			t.Fatal(err)
		}
		live := filepath.Join(t.TempDir(), "live.toml")
		if err := os.WriteFile(live, []byte("[tools]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(live, filepath.Join(miseState, dir, "live")); err != nil {
			t.Fatal(err)
		}
	}

	baselines := Baselines{Dir: paths.StateDir}
	if err := baselines.Save(dockID, json.RawMessage(`["/Applications/Example.app"]`)); err != nil {
		t.Fatal(err)
	}
	if err := baselines.Save(chromePWAsID, json.RawMessage(`["abcdefghijklmnopabcdefghijklmnop"]`)); err != nil {
		t.Fatal(err)
	}

	currentID := strings.Repeat("1", 32)
	oldID := strings.Repeat("2", 32)
	for _, state := range []struct {
		id     string
		status string
	}{{currentID, restoreCompleteState}, {oldID, restoreCompleteState}} {
		progress := restoreProgress{paths: paths, record: restoreRecord{
			Schema: restoreSchema, Repository: "github.com/example/machine", Checkout: state.id,
			Commit: strings.Repeat("a", 40), Plan: "sha256:" + strings.Repeat("b", 64), Status: state.status,
		}}
		if err := progress.save(); err != nil {
			t.Fatal(err)
		}
	}

	hookBody := []byte("#!/bin/sh\nexit 0\n")
	hookPath := filepath.Join(hooks, "post-checkout")
	if err := os.WriteFile(hookPath, hookBody, 0o755); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(hooks, repositoryHookManifestName)
	manifest := repositoryHookManifest{
		Schema: repositoryHookManifestSchema,
		Hooks:  map[string]string{"post-checkout": repositoryHookDigest(hookBody)},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := &pruneStubRunner{
		root:               paths.Root,
		currentRestore:     currentID,
		prunableTools:      `{"github:azohra/config":[{"version":"0.5.0"}],"node":[{"version":"23.0.0"}]}`,
		packageStatus:      `{"mas":{"available":true},"brew":{"available":true}}`,
		prunableToolStderr: "mise WARN stale tracked configuration\n",
		packageDryRuns: map[string]Result{
			"brew": {Stderr: "mise brew: would remove orphan-package\n"},
			"mas":  {Stderr: "mise ERROR package manager 'mas' does not support pruning\n", Err: pruneExitError(t)},
		},
	}
	live := &pruneCommandRecorder{}
	var output bytes.Buffer
	machine := testMachine()
	machine.Dock = false
	machine.ChromePWAs = false
	machine.RepositoryHooks = nil
	pruner := Pruner{
		Paths: paths, Machine: machine, Runner: runner, Live: live,
		MiseStateDir: miseState, Log: Logger{Out: &output},
	}
	return pruneFixture{
		pruner: pruner, live: live,
		dockBaseline:   filepath.Join(paths.StateDir, dockID+".json"),
		chromeBaseline: filepath.Join(paths.StateDir, chromePWAsID+".json"),
		oldRestore:     restoreStatePath(paths, oldID), currentRestore: restoreStatePath(paths, currentID),
		hook: hookPath, manifest: manifestPath,
	}
}

func pruneExitError(t *testing.T) error {
	t.Helper()
	return exec.Command("sh", "-c", "exit 1").Run()
}

func TestNewPrunerPinsPlanningAndApplyToTheSameMiseState(t *testing.T) {
	paths := testPaths(t)
	t.Setenv("MISE_STATE_DIR", "relative-mise-state")
	pruner := NewPruner(paths, testMachine(), &bytes.Buffer{})
	want := filepath.Join(paths.Root, "relative-mise-state")
	if pruner.MiseStateDir != want {
		t.Fatalf("mise state directory = %q, want %q", pruner.MiseStateDir, want)
	}
	runner, ok := pruner.Runner.(OSRunner)
	if !ok || !slices.Contains(runner.Environment, "MISE_STATE_DIR="+want) {
		t.Fatalf("planning environment = %#v", pruner.Runner)
	}
	live, ok := pruner.Live.(LiveRunner)
	if !ok || !slices.Contains(live.Environment, "MISE_STATE_DIR="+want) {
		t.Fatalf("apply environment = %#v", pruner.Live)
	}
}

func TestPrunePlanUsesMiseInventoryAndProvesConfigOwnership(t *testing.T) {
	fixture := newPruneFixture(t)
	plan, err := fixture.pruner.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.registry.Tracked) != 1 || len(plan.registry.Trusted) != 1 {
		t.Fatalf("registry plan = %+v", plan.registry)
	}
	if got := plan.tools; !slices.Equal(got, []pruneTool{{"github:azohra/config", "0.5.0"}, {"node", "23.0.0"}}) {
		t.Fatalf("tool plan = %+v", got)
	}
	if len(plan.packages) != 1 || plan.packages[0].Name != "brew" ||
		!slices.Equal(plan.packages[0].Preview, []string{"would remove orphan-package"}) {
		t.Fatalf("package plan = %+v", plan.packages)
	}
	if !slices.Equal(plan.skippedManagers, []string{"mas"}) {
		t.Fatalf("skipped managers = %v", plan.skippedManagers)
	}
	if len(plan.hooks) != 1 || len(plan.hooks[0].Hooks) != 1 || !plan.hooks[0].Hooks[0].RemoveFile {
		t.Fatalf("hook plan = %+v", plan.hooks)
	}
	if len(plan.files) != 3 {
		t.Fatalf("Config file plan = %+v", plan.files)
	}
	if len(plan.warnings) != 1 || !strings.Contains(plan.warnings[0], "mise WARN") {
		t.Fatalf("warnings = %v", plan.warnings)
	}

	var output bytes.Buffer
	WritePrunePlan(&output, plan)
	for _, want := range []string{
		"1 dead tracked link", "1 dead trust link", "github:azohra/config@0.5.0",
		"brew: would remove orphan-package", "post-checkout", "Dock baseline",
		"mas does not support pruning; left untouched", "mise WARN stale tracked configuration",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("prune preview does not contain %q:\n%s", want, output.String())
		}
	}
}

func TestPruneApplyRechecksThenUsesOwnedDeletionPaths(t *testing.T) {
	fixture := newPruneFixture(t)
	plan, err := fixture.pruner.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.pruner.Apply(plan); err != nil {
		t.Fatal(err)
	}
	wantCommands := []string{
		"mise prune --configs --yes",
		"mise prune --tools --yes",
		"mise bootstrap packages prune --manager brew --yes",
	}
	if !slices.Equal(fixture.live.commands, wantCommands) {
		t.Fatalf("apply commands = %v, want %v", fixture.live.commands, wantCommands)
	}
	for _, path := range []string{fixture.dockBaseline, fixture.chromeBaseline, fixture.oldRestore, fixture.hook, fixture.manifest} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("pruned path still exists: %s (%v)", path, err)
		}
	}
	if _, err := os.Lstat(fixture.currentRestore); err != nil {
		t.Fatalf("current restore record was removed: %v", err)
	}
}

func TestPruneApplyRejectsAChangedPlanBeforeMutation(t *testing.T) {
	fixture := newPruneFixture(t)
	plan, err := fixture.pruner.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if err := (Baselines{Dir: fixture.pruner.Paths.StateDir}).Save(dockID, json.RawMessage(`[]`)); err != nil {
		t.Fatal(err)
	}
	err = fixture.pruner.Apply(plan)
	if err == nil || !strings.Contains(err.Error(), "prune plan changed") {
		t.Fatalf("changed plan apply = %v", err)
	}
	if len(fixture.live.commands) != 0 {
		t.Fatalf("changed plan ran commands: %v", fixture.live.commands)
	}
	for _, path := range []string{fixture.dockBaseline, fixture.oldRestore, fixture.hook} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("changed plan removed %s: %v", path, err)
		}
	}
}

func TestPrunePreservesAHookThatChangedAfterConfigInstalledIt(t *testing.T) {
	fixture := newPruneFixture(t)
	if err := os.WriteFile(fixture.hook, []byte("#!/bin/sh\necho repository owned\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan, err := fixture.pruner.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.hooks) != 0 {
		t.Fatalf("changed hook was planned for deletion: %+v", plan.hooks)
	}
	if !slices.ContainsFunc(plan.warnings, func(warning string) bool {
		return strings.Contains(warning, "changed since Config installed it; left untouched")
	}) {
		t.Fatalf("changed hook warning missing: %v", plan.warnings)
	}
	if _, err := os.Lstat(fixture.hook); err != nil {
		t.Fatalf("changed hook was removed: %v", err)
	}
}

func TestDeadConfigLinksIgnoreLiveLinksAndNonLinks(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(t.TempDir(), "mise.toml")
	if err := os.WriteFile(live, []byte("[tools]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(live, filepath.Join(dir, "live")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), filepath.Join(dir, "dead")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "foreign"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	links, err := deadConfigLinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || filepath.Base(links[0].Path) != "dead" {
		t.Fatalf("dead links = %+v", links)
	}
}
