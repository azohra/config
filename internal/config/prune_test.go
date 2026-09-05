package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

type unavailableRunner struct{}

func (unavailableRunner) Run(context.Context, string, ...string) Result {
	return Result{Err: exec.ErrNotFound}
}
func (unavailableRunner) Exists(string) bool { return false }

func (r *pruneCommandRecorder) Command(name string, args ...string) error {
	r.commands = append(r.commands, strings.Join(append([]string{name}, args...), " "))
	return nil
}

type pruneFixture struct {
	pruner         Pruner
	live           *pruneCommandRecorder
	dockBaseline   string
	chromeBaseline string
	finderBaseline string
	oldRestore     string
	currentRestore string
	hook           string
	manifest       string
	caskDir        string
	bottleDir      string
	cask           string
	bottle         string
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

	miseCache := t.TempDir()
	caskDir := filepath.Join(miseCache, "system-brew", "casks")
	bottleDir := filepath.Join(miseCache, "system-brew", "bottles")
	for _, dir := range []string{caskDir, bottleDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cask := filepath.Join(caskDir, "example-1.0-abc123-Example.dmg")
	if err := os.WriteFile(cask, bytes.Repeat([]byte("c"), 2048), 0o644); err != nil {
		t.Fatal(err)
	}
	bottle := filepath.Join(bottleDir, "example-1.0.tar.gz")
	if err := os.WriteFile(bottle, bytes.Repeat([]byte("b"), 1024), 0o644); err != nil {
		t.Fatal(err)
	}

	baselines := Baselines{Dir: paths.StateDir}
	if err := baselines.Save(dockID, json.RawMessage(`["/Applications/Example.app"]`)); err != nil {
		t.Fatal(err)
	}
	if err := baselines.Save(chromePWAsID, json.RawMessage(`["abcdefghijklmnopabcdefghijklmnop"]`)); err != nil {
		t.Fatal(err)
	}
	if err := baselines.Save(finderFavoritesID, json.RawMessage(`[{"name":"Development","path":"/Users/example/Development"}]`)); err != nil {
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
		Hooks:  map[string]string{"post-checkout": contentDigest(hookBody)},
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
		Paths: paths, Machine: machine, Runner: runner, Mise: runner, MiseLive: live,
		MiseStateDir: miseState, MiseCacheDir: miseCache, Log: Logger{Out: &output},
	}
	return pruneFixture{
		pruner: pruner, live: live,
		dockBaseline:   filepath.Join(paths.StateDir, dockID+".json"),
		chromeBaseline: filepath.Join(paths.StateDir, chromePWAsID+".json"),
		finderBaseline: filepath.Join(paths.StateDir, finderFavoritesID+".json"),
		oldRestore:     restoreStatePath(paths, oldID), currentRestore: restoreStatePath(paths, currentID),
		hook: hookPath, manifest: manifestPath,
		caskDir: caskDir, bottleDir: bottleDir, cask: cask, bottle: bottle,
	}
}

func pruneExitError(t *testing.T) error {
	t.Helper()
	return exec.Command("sh", "-c", "exit 1").Run()
}

func TestNewPrunerLeavesMiseStateToMise(t *testing.T) {
	// Config once derived this path from the ambient environment and forced it
	// onto the subprocesses that delete tools and packages, so an exported
	// MISE_STATE_DIR — a relative one especially — decided what got deleted.
	paths := testPaths(t)
	t.Setenv("MISE_STATE_DIR", "relative-mise-state")
	pruner := NewPruner(paths, testMachine(), &bytes.Buffer{})
	if pruner.MiseStateDir != "" || pruner.MiseCacheDir != "" {
		t.Fatalf("pruner derived mise directories: %q, %q", pruner.MiseStateDir, pruner.MiseCacheDir)
	}
	runner, ok := pruner.Mise.(OSRunner)
	if !ok {
		t.Fatalf("Mise planning runner = %#v", pruner.Mise)
	}
	live, ok := pruner.MiseLive.(LiveRunner)
	if !ok {
		t.Fatalf("Mise apply runner = %#v", pruner.MiseLive)
	}
	for _, environment := range [][]string{runner.Environment, live.Environment} {
		for _, entry := range environment {
			for _, forced := range []string{"MISE_STATE_DIR=", "MISE_CACHE_DIR="} {
				if strings.HasPrefix(entry, forced) {
					t.Fatalf("Config forced a mise directory onto its own subprocess: %q", entry)
				}
			}
		}
	}
}

func TestPrunerStillPlansConfigStateWhenMiseIsUnavailable(t *testing.T) {
	fixture := newPruneFixture(t)
	fixture.pruner.Mise = unavailableRunner{}
	plan, err := fixture.pruner.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.files) == 0 || len(plan.hooks) == 0 {
		t.Fatalf("Config-owned cleanup disappeared with Mise: files=%v hooks=%v", plan.files, plan.hooks)
	}
	if !slices.ContainsFunc(plan.warnings, func(warning string) bool {
		return strings.Contains(warning, "Mise repository inventory is unavailable")
	}) {
		t.Fatalf("missing Mise warning: %v", plan.warnings)
	}
}

func TestPrunerDoesNotProbeUndeclaredMise(t *testing.T) {
	machine := testMachine()
	machine.Mise = false
	plan, err := (Pruner{
		Paths: testPaths(t), Machine: machine, Runner: converged{},
		Mise: unavailableRunner{}, Log: Logger{Out: &bytes.Buffer{}},
	}).Plan()
	if err != nil {
		t.Fatal(err)
	}
	for _, warning := range plan.warnings {
		if strings.Contains(strings.ToLower(warning), "mise") {
			t.Fatalf("undeclared Mise produced warning %q", warning)
		}
	}
}

func TestPrunerAsksMiseWhereItsStateAndCacheLive(t *testing.T) {
	answers := map[string]Result{
		"doctor": {Stdout: `{"dirs":{"state":"/var/db/mise-state","cache":"/var/db/mise-cache"}}`},
	}
	state, cache, err := miseDirs(stubRunner{answers: answers})
	if err != nil {
		t.Fatal(err)
	}
	if state != "/var/db/mise-state" || cache != "/var/db/mise-cache" {
		t.Fatalf("mise directories = %q, %q", state, cache)
	}
	for name, answer := range map[string]Result{
		"relative state": {Stdout: `{"dirs":{"state":"mise-state","cache":"/var/db/mise-cache"}}`},
		"relative cache": {Stdout: `{"dirs":{"state":"/var/db/mise-state","cache":"mise-cache"}}`},
		"absent state":   {Stdout: `{"dirs":{"cache":"/var/db/mise-cache"}}`},
		"absent cache":   {Stdout: `{"dirs":{"state":"/var/db/mise-state"}}`},
		"absent both":    {Stdout: `{"dirs":{}}`},
		"garbage":        {Stdout: "not a document"},
	} {
		if _, _, err := miseDirs(stubRunner{answers: map[string]Result{"doctor": answer}}); err == nil {
			t.Errorf("%s answer accepted as a mise directory", name)
		}
	}
}

// stubRunner answers one command by the first argument that matches a key.
type stubRunner struct{ answers map[string]Result }

func (r stubRunner) Run(_ context.Context, _ string, args ...string) Result {
	for _, arg := range args {
		if answer, ok := r.answers[arg]; ok {
			return answer
		}
	}
	return Result{Err: errors.New("unexpected command")}
}

func (stubRunner) Exists(string) bool { return true }

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
	if len(plan.caches) != 2 ||
		plan.caches[0].Label != "Homebrew cask downloads" || plan.caches[0].bytes() != 2048 ||
		plan.caches[1].Label != "Homebrew bottle downloads" || plan.caches[1].bytes() != 1024 {
		t.Fatalf("download cache plan = %+v", plan.caches)
	}
	if len(plan.hooks) != 1 || len(plan.hooks[0].Hooks) != 1 || !plan.hooks[0].Hooks[0].RemoveFile {
		t.Fatalf("hook plan = %+v", plan.hooks)
	}
	if len(plan.files) != 4 {
		t.Fatalf("Config file plan = %+v", plan.files)
	}
	if len(plan.warnings) != 1 || !strings.Contains(plan.warnings[0], "mise WARN") {
		t.Fatalf("warnings = %v", plan.warnings)
	}

	var output bytes.Buffer
	WritePrunePlan(&output, plan)
	for _, want := range []string{
		"1 dead tracked link", "1 dead trust link", "github:azohra/config@0.5.0",
		"brew: would remove orphan-package", "post-checkout", "Dock baseline", "Finder Favorites baseline",
		"Homebrew cask downloads: 1 file, 2.00 KiB", "Homebrew bottle downloads: 1 file, 1.00 KiB",
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
	for _, path := range []string{
		fixture.dockBaseline, fixture.chromeBaseline, fixture.finderBaseline,
		fixture.oldRestore, fixture.hook, fixture.manifest, fixture.cask, fixture.bottle,
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("pruned path still exists: %s (%v)", path, err)
		}
	}
	if _, err := os.Lstat(fixture.currentRestore); err != nil {
		t.Fatalf("current restore record was removed: %v", err)
	}
	// The directories belong to mise; only the downloads inside them are ours
	// to reclaim.
	for _, dir := range []string{fixture.caskDir, fixture.bottleDir} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("prune removed a mise cache directory: %s (%v)", dir, err)
		}
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

func TestPruneRefusesAHookWhoseOwnershipMovedAfterPreview(t *testing.T) {
	dir := t.TempDir()
	body := []byte("#!/bin/sh\nexit 0\n")
	digest := contentDigest(body)
	hookPath := filepath.Join(dir, "post-checkout")
	manifestPath := filepath.Join(dir, repositoryHookManifestName)
	writeManifest := func(hookDigest string) []byte {
		t.Helper()
		encoded, err := json.MarshalIndent(repositoryHookManifest{Schema: repositoryHookManifestSchema, Hooks: map[string]string{"post-checkout": hookDigest}}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, '\n')
		if err := os.WriteFile(manifestPath, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	manifestBytes := writeManifest(digest)
	if err := os.WriteFile(hookPath, body, 0o755); err != nil {
		t.Fatal(err)
	}
	target := pruneHookTarget{
		Name: "a repository", Dir: dir, ManifestDigest: contentDigest(manifestBytes),
		Hooks: []pruneHook{{Name: "post-checkout", Digest: digest, RemoveFile: true}},
	}

	// The manifest moved: something re-claimed the hook after the preview.
	writeManifest(contentDigest([]byte("#!/bin/sh\nexit 1\n")))
	if err := applyPruneHooks(target); err == nil {
		t.Fatal("a hook whose ownership moved after preview was deleted")
	}
	if _, err := os.Stat(hookPath); err != nil {
		t.Fatalf("the refused hook was removed: %v", err)
	}

	// The manifest is back, but the hook body itself changed.
	manifestBytes = writeManifest(digest)
	target.ManifestDigest = contentDigest(manifestBytes)
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\necho mine\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := applyPruneHooks(target); err == nil {
		t.Fatal("a hook that changed after preview was deleted")
	}
	if _, err := os.Stat(hookPath); err != nil {
		t.Fatalf("the refused hook was removed: %v", err)
	}

	// Unchanged: the hook and the manifest that claimed it both go.
	if err := os.WriteFile(hookPath, body, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := applyPruneHooks(target); err != nil {
		t.Fatalf("an unchanged hook was refused: %v", err)
	}
	if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
		t.Fatal("an unchanged hook survived the prune")
	}
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Fatal("an emptied ownership manifest survived the prune")
	}
}

func TestFormatBytesReportsTheUnitsTheseFilesAreMeasuredIn(t *testing.T) {
	for _, want := range []struct {
		bytes int64
		text  string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.00 KiB"},
		{1024*1024 - 1, "1024.00 KiB"},
		{1024 * 1024, "1.00 MiB"},
		{7657869475, "7.13 GiB"},
		{1024 * 1024 * 1024 * 1024, "1.00 TiB"},
		{1024 * 1024 * 1024 * 1024 * 1024, "1.00 PiB"},
	} {
		if got := FormatBytes(want.bytes); got != want.text {
			t.Errorf("FormatBytes(%d) = %q, want %q", want.bytes, got, want.text)
		}
	}
}

func TestPruneCachePlansOnlyDownloadsAndOnlyWhereMiseKeepsThem(t *testing.T) {
	fixture := newPruneFixture(t)
	pruner := fixture.pruner

	// A machine that never installed a Homebrew package has no such directory.
	caches, warnings, err := pruner.planCaches(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(caches) != 0 || len(warnings) != 0 {
		t.Fatalf("absent cache planned %+v with warnings %v", caches, warnings)
	}

	// cask-extract is live during an install, so nothing in it is a candidate.
	extract := filepath.Join(pruner.MiseCacheDir, "system-brew", "cask-extract", "example-1.0")
	if err := os.MkdirAll(extract, 0o755); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(filepath.Dir(extract), "Example.app.partial")
	if err := os.WriteFile(staged, []byte("mid-install"), 0o644); err != nil {
		t.Fatal(err)
	}
	caches, warnings, err = pruner.planCaches(pruner.MiseCacheDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	for _, cache := range caches {
		if strings.Contains(cache.Dir, "cask-extract") {
			t.Fatalf("the cask staging directory was planned for deletion: %+v", cache)
		}
	}
	if len(caches) != 2 || len(caches[0].Files) != 1 || caches[0].Files[0].Size != 2048 {
		t.Fatalf("cache plan = %+v", caches)
	}
	for _, path := range []string{extract, staged} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("planning disturbed the staging area: %s (%v)", path, err)
		}
	}
}

func TestPruneCacheLeavesEverythingThatIsNotAFinishedDownload(t *testing.T) {
	fixture := newPruneFixture(t)
	nested := filepath.Join(fixture.caskDir, "partial")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(fixture.caskDir, "elsewhere.dmg")
	if err := os.Symlink(fixture.bottle, link); err != nil {
		t.Fatal(err)
	}
	caches, warnings, err := fixture.pruner.planCaches(fixture.pruner.MiseCacheDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(caches) != 2 || len(caches[0].Files) != 1 || caches[0].Files[0].Name != filepath.Base(fixture.cask) {
		t.Fatalf("cache plan = %+v", caches)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "not downloaded files; left untouched") {
		t.Fatalf("warnings = %v", warnings)
	}
	if _, err := applyPruneCache(caches[0]); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{nested, link} {
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("prune removed %s: %v", path, err)
		}
	}
}

func TestPruneApplyRejectsAPlanADownloadArrivedAfter(t *testing.T) {
	// An install finishing between preview and apply adds a file the user
	// never saw in the plan.
	fixture := newPruneFixture(t)
	plan, err := fixture.pruner.Plan()
	if err != nil {
		t.Fatal(err)
	}
	arrival := filepath.Join(fixture.caskDir, "newcomer-2.0-def456-Newcomer.dmg")
	if err := os.WriteFile(arrival, bytes.Repeat([]byte("n"), 512), 0o644); err != nil {
		t.Fatal(err)
	}
	err = fixture.pruner.Apply(plan)
	if err == nil || !strings.Contains(err.Error(), "prune plan changed") {
		t.Fatalf("changed cache apply = %v", err)
	}
	for _, path := range []string{arrival, fixture.cask, fixture.bottle} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("a refused plan removed %s: %v", path, err)
		}
	}
}

func TestPruneApplyRejectsAPlanADownloadGrewUnder(t *testing.T) {
	// Nothing revalidates a file at the moment of deletion, so the recompute
	// is what has to keep an install from losing what it is fetching.
	fixture := newPruneFixture(t)
	plan, err := fixture.pruner.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.cask, bytes.Repeat([]byte("c"), 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	err = fixture.pruner.Apply(plan)
	if err == nil || !strings.Contains(err.Error(), "prune plan changed") {
		t.Fatalf("apply over a growing download = %v", err)
	}
	for _, path := range []string{fixture.cask, fixture.bottle} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("a refused plan removed %s: %v", path, err)
		}
	}
}

func TestPruneCacheCountsWhatItActuallyReclaimed(t *testing.T) {
	fixture := newPruneFixture(t)
	caches, _, err := fixture.pruner.planCaches(fixture.pruner.MiseCacheDir, nil)
	if err != nil {
		t.Fatal(err)
	}

	// A download that vanished in between is the outcome this wanted, and its
	// bytes were not reclaimed here.
	if err := os.Remove(fixture.bottle); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := applyPruneCache(caches[1])
	if err != nil {
		t.Fatalf("a download that vanished was reported as a failure: %v", err)
	}
	if reclaimed != 0 {
		t.Fatalf("reclaimed = %d for a file that was already gone", reclaimed)
	}

	reclaimed, err = applyPruneCache(caches[0])
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed != 2048 {
		t.Fatalf("reclaimed = %d, want 2048", reclaimed)
	}
	if _, err := os.Stat(fixture.cask); !os.IsNotExist(err) {
		t.Fatal("a planned download survived the prune")
	}
}

func TestPruneReclaimsOnlyTheMarkersNothingWillActOn(t *testing.T) {
	// A marker records a step a killed run still owes. One the document no
	// longer declares is a step nothing will ever take, and nothing else
	// removes it.
	fixture := newPruneFixture(t)
	pruner := fixture.pruner
	pruner.Machine.Dock = true
	pruner.Machine.Preferences = []PreferenceBackup{{
		ID: "example-app", Name: "Example App",
		Bundle: "com.example.ExampleApp", Domain: "com.example.ExampleApp",
	}}
	for _, name := range []string{
		dockRestartMarker,
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
	if len(planned) != 1 || filepath.Base(planned[0].Path) != relaunchMarker("com.example.Retired") {
		t.Fatalf("planned = %+v, want only the undeclared relaunch", planned)
	}
	if err := applyPruneFile(planned[0]); err != nil {
		t.Fatal(err)
	}
	if markerSet(pruner.Paths, relaunchMarker("com.example.Retired")) {
		t.Fatal("the undeclared marker survived the prune")
	}
	for _, kept := range []string{dockRestartMarker, relaunchMarker("com.example.ExampleApp")} {
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
