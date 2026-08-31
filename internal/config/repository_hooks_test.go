package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type repositoryHooksRunner struct {
	configuration string
	repositories  map[string]string
	commonDirs    map[string]string
	hookDirs      map[string]string
}

func (r repositoryHooksRunner) Run(_ context.Context, name string, args ...string) Result {
	switch {
	case name == "mise" && slices.Equal(args, []string{"--version"}):
		return Result{Stdout: testedMiseVersion}
	case name == "mise" && slices.Equal(args, []string{"config", "ls", "-J"}):
		data, _ := json.Marshal([]map[string]string{{"path": r.configuration}})
		return Result{Stdout: string(data)}
	case name == "mise" && len(args) == 5 && args[0] == "config" && args[1] == "get":
		var lines []string
		for path, url := range r.repositories {
			lines = append(lines, fmt.Sprintf("%q = { url = %q }", path, url))
		}
		slices.Sort(lines)
		return Result{Stdout: strings.Join(lines, "\n")}
	case name == "git" && len(args) == 5 && args[0] == "-C" && args[2] == "rev-parse" && args[4] == "--git-common-dir":
		common, found := r.commonDirs[args[1]]
		if !found {
			return Result{Err: fmt.Errorf("unknown repository %s", args[1])}
		}
		return Result{Stdout: common + "\n"}
	case name == "git" && len(args) == 6 && args[0] == "-C" && args[2] == "rev-parse" && args[4] == "--git-path" && args[5] == "hooks":
		dir := r.hookDirs[args[1]]
		if dir == "" {
			common, found := r.commonDirs[args[1]]
			if !found {
				return Result{Err: fmt.Errorf("unknown repository %s", args[1])}
			}
			dir = filepath.Join(common, "hooks")
		}
		return Result{Stdout: dir + "\n"}
	default:
		return Result{Err: fmt.Errorf("unexpected command: %s %v", name, args)}
	}
}

func (repositoryHooksRunner) Exists(string) bool { return true }

type repositoryHooksFixture struct {
	paths   Paths
	machine Machine
	runner  repositoryHooksRunner
	repo    string
	source  string
}

func newRepositoryHooksFixture(t *testing.T) repositoryHooksFixture {
	t.Helper()
	paths := testPaths(t)
	for _, dir := range []string{paths.InRoot(".git", "hooks"), paths.InRoot("hooks")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	source := paths.InRoot("hooks", "post-checkout")
	if err := os.WriteFile(source, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(paths.Home, "Development", "example")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	machine := testMachine()
	machine.RepositoryHooks = []RepositoryHook{{Name: "post-checkout", Source: "hooks/post-checkout"}}
	return repositoryHooksFixture{
		paths: paths, machine: machine, repo: repo, source: source,
		runner: repositoryHooksRunner{
			configuration: paths.InRoot("mise", "repositories.toml"),
			repositories:  map[string]string{repo: "https://example.com/example.git"},
			commonDirs: map[string]string{
				paths.Root: paths.InRoot(".git"),
				repo:       filepath.Join(repo, ".git"),
			},
		},
	}
}

func TestRepositoryHookDeclarationRequiresARegularExecutableSource(t *testing.T) {
	fixture := newRepositoryHooksFixture(t)
	if _, err := repositoryHookPayloads(fixture.paths, fixture.machine.RepositoryHooks); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fixture.source, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := repositoryHookPayloads(fixture.paths, fixture.machine.RepositoryHooks); err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Fatalf("non-executable source error = %v", err)
	}
	if err := os.Remove(fixture.source); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(fixture.paths.Home, "outside")
	if err := os.WriteFile(outside, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, fixture.source); err != nil {
		t.Fatal(err)
	}
	if _, err := repositoryHookPayloads(fixture.paths, fixture.machine.RepositoryHooks); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink source error = %v", err)
	}
}

func TestRepositoryHooksReconcileTemplateAndDeclaredRepositories(t *testing.T) {
	fixture := newRepositoryHooksFixture(t)
	templateHook := filepath.Join(repositoryHookTemplateDir(fixture.paths), "hooks", "post-checkout")
	if err := os.MkdirAll(filepath.Dir(templateHook), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fixture.source, templateHook); err != nil {
		t.Fatal(err)
	}
	repoHook := filepath.Join(fixture.repo, ".git", "hooks", "post-checkout")
	if err := os.Symlink(fixture.source, repoHook); err != nil {
		t.Fatal(err)
	}
	managedRootHook := fixture.paths.InRoot(".git", "hooks", "post-checkout")
	if err := os.WriteFile(managedRootHook, []byte("#!/bin/sh\n"+legacyRepositoryHookMarker+"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	resource := InspectRepositoryHooks(fixture.paths, fixture.machine, fixture.runner)
	if resource.State != Drift || !resource.Allows(Apply) {
		t.Fatalf("legacy hooks = %+v", resource)
	}
	applier := Applier{Paths: fixture.paths, Machine: fixture.machine, Runner: fixture.runner, Log: Logger{Out: io.Discard}}
	changed, err := applier.applyRepositoryHookTargets(true)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 3 {
		t.Fatalf("changed = %d, want 3", changed)
	}
	want, err := os.ReadFile(fixture.source)
	if err != nil {
		t.Fatal(err)
	}
	for _, hook := range []string{templateHook, managedRootHook, repoHook} {
		info, err := os.Lstat(hook)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o755 {
			t.Errorf("%s mode = %v", hook, info.Mode())
		}
		got, err := os.ReadFile(hook)
		if err != nil || string(got) != string(want) {
			t.Errorf("%s body = %q, %v", hook, got, err)
		}
		manifest, err := readRepositoryHookManifest(filepath.Dir(hook))
		if err != nil || manifest.Hooks["post-checkout"] != repositoryHookDigest(want) {
			t.Errorf("%s manifest = %+v, %v", hook, manifest, err)
		}
	}
	if current := InspectRepositoryHooks(fixture.paths, fixture.machine, fixture.runner); current.State != Current {
		t.Fatalf("reconciled hooks = %+v", current)
	}
}

func TestRepositoryHooksRefreshOnlyCopiesConfigStillOwns(t *testing.T) {
	fixture := newRepositoryHooksFixture(t)
	applier := Applier{Paths: fixture.paths, Machine: fixture.machine, Runner: fixture.runner, Log: Logger{Out: io.Discard}}
	if _, err := applier.applyRepositoryHookTargets(true); err != nil {
		t.Fatal(err)
	}
	updated := []byte("#!/bin/sh\nprintf updated\n")
	if err := atomicWrite(fixture.source, updated, 0o755); err != nil {
		t.Fatal(err)
	}
	if resource := InspectRepositoryHooks(fixture.paths, fixture.machine, fixture.runner); resource.State != Drift || !resource.Allows(Apply) {
		t.Fatalf("changed declaration = %+v", resource)
	}
	if _, err := applier.applyRepositoryHookTargets(true); err != nil {
		t.Fatal(err)
	}
	repoHook := filepath.Join(fixture.repo, ".git", "hooks", "post-checkout")
	if got, err := os.ReadFile(repoHook); err != nil || string(got) != string(updated) {
		t.Fatalf("updated hook = %q, %v", got, err)
	}

	foreign := []byte("#!/bin/sh\nexec ./repository-owned\n")
	if err := atomicWrite(repoHook, foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	resource := InspectRepositoryHooks(fixture.paths, fixture.machine, fixture.runner)
	if resource.State != Drift || resource.Allows(Apply) || resource.Failed() == 0 {
		t.Fatalf("foreign hook = %+v", resource)
	}
	templateHook := filepath.Join(repositoryHookTemplateDir(fixture.paths), "hooks", "post-checkout")
	templateBefore, err := os.ReadFile(templateHook)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applier.applyRepositoryHookTargets(true); err == nil || !strings.Contains(err.Error(), "repository-owned hook") {
		t.Fatalf("foreign hook apply error = %v", err)
	}
	if templateAfter, err := os.ReadFile(templateHook); err != nil || string(templateAfter) != string(templateBefore) {
		t.Fatalf("apply mutated another target before refusing conflict: %q, %v", templateAfter, err)
	}
	if got, err := os.ReadFile(repoHook); err != nil || string(got) != string(foreign) {
		t.Fatalf("foreign hook was overwritten: %q, %v", got, err)
	}
}

func TestRepositoryHooksPreserveRepositoryOwnedHookConfiguration(t *testing.T) {
	t.Run("core hooks path", func(t *testing.T) {
		fixture := newRepositoryHooksFixture(t)
		fixture.runner.hookDirs = map[string]string{
			fixture.repo: filepath.Join(fixture.repo, "repository-hooks"),
		}
		resource := InspectRepositoryHooks(fixture.paths, fixture.machine, fixture.runner)
		if resource.State != Drift || resource.Allows(Apply) || resource.Failed() == 0 ||
			!strings.Contains(strings.Join(resource.Details, "\n"), "core.hooksPath") {
			t.Fatalf("redirected hooks = %+v", resource)
		}
		applier := Applier{Paths: fixture.paths, Machine: fixture.machine, Runner: fixture.runner, Log: Logger{Out: io.Discard}}
		if _, err := applier.applyRepositoryHookTargets(true); err == nil || !strings.Contains(err.Error(), "core.hooksPath") {
			t.Fatalf("redirected hook apply error = %v", err)
		}
	})

	t.Run("hooks directory symlink", func(t *testing.T) {
		fixture := newRepositoryHooksFixture(t)
		hooksDir := filepath.Join(fixture.repo, ".git", "hooks")
		if err := os.Remove(hooksDir); err != nil {
			t.Fatal(err)
		}
		external := filepath.Join(fixture.paths.Home, "repository-hooks")
		if err := os.MkdirAll(external, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, hooksDir); err != nil {
			t.Fatal(err)
		}
		resource := InspectRepositoryHooks(fixture.paths, fixture.machine, fixture.runner)
		if resource.State != Drift || resource.Allows(Apply) || resource.Failed() == 0 ||
			!strings.Contains(strings.Join(resource.Details, "\n"), "directory is a repository-owned symlink") {
			t.Fatalf("linked hooks directory = %+v", resource)
		}
		if _, err := os.Stat(filepath.Join(external, "post-checkout")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("external hooks directory was changed: %v", err)
		}
	})
}

func TestRepositoryHookTemplateEnvironmentIsProcessScoped(t *testing.T) {
	paths := testPaths(t)
	want := []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=init.templateDir",
		"GIT_CONFIG_VALUE_0=" + repositoryHookTemplateDir(paths),
	}
	if got := repositoryHookTemplateEnvironment(paths); !slices.Equal(got, want) {
		t.Fatalf("template environment = %v, want %v", got, want)
	}
}

func TestGitCopiesThePreparedHookAndOwnershipManifest(t *testing.T) {
	fixture := newRepositoryHooksFixture(t)
	applier := Applier{Paths: fixture.paths, Machine: fixture.machine, Runner: fixture.runner, Log: Logger{Out: io.Discard}}
	if _, err := applier.applyRepositoryHookTargets(false); err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(fixture.paths.Home, "fresh")
	command := exec.Command("git", "init", "--quiet", repository)
	command.Env = ChildEnvironment(repositoryHookTemplateEnvironment(fixture.paths))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	for _, name := range []string{"post-checkout", repositoryHookManifestName} {
		if _, err := os.Stat(filepath.Join(repository, ".git", "hooks", name)); err != nil {
			t.Errorf("template did not install %s: %v", name, err)
		}
	}
}

func TestApplyMisePreparesAndThenSweepsRepositoryHooks(t *testing.T) {
	fixture := newRepositoryHooksFixture(t)
	fixture.machine.MacOS = MachineMacOS{}
	fakeBin := t.TempDir()
	log := filepath.Join(t.TempDir(), "mise-environment")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MISE_ENVIRONMENT_LOG", log)
	script := `#!/bin/sh
printf '%s\n%s\n%s\n%s\n' "$GIT_CONFIG_COUNT" "$GIT_CONFIG_KEY_0" "$GIT_CONFIG_VALUE_0" "$*" > "$MISE_ENVIRONMENT_LOG"
`
	if err := os.WriteFile(filepath.Join(fakeBin, "mise"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	applier := Applier{
		Paths: fixture.paths, Machine: fixture.machine, Runner: fixture.runner,
		Live: LiveRunner{Stdout: io.Discard, Stderr: io.Discard}, Log: Logger{Out: io.Discard},
	}
	if err := applier.applyMise(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"1", "init.templateDir", repositoryHookTemplateDir(fixture.paths), "bootstrap --yes --skip-dirty", "",
	}, "\n")
	if string(data) != want {
		t.Fatalf("mise environment = %q, want %q", data, want)
	}
	for _, hook := range []string{
		fixture.paths.InRoot(".git", "hooks", "post-checkout"),
		filepath.Join(fixture.repo, ".git", "hooks", "post-checkout"),
	} {
		if _, err := os.Stat(hook); err != nil {
			t.Errorf("post-bootstrap sweep missed %s: %v", hook, err)
		}
	}
}

func TestRepositoryHooksClaimOwnershipBeforeInstalling(t *testing.T) {
	// An interrupted apply must not leave an executable hook that no ownership
	// record accounts for: prune only sees hooks its manifest names, so an
	// orphan is invisible to it and to every later refresh. Making the target
	// unwritable stops the apply at its first write, which must be the claim.
	fixture := newRepositoryHooksFixture(t)
	hooks := fixture.paths.InRoot(".git", "hooks")
	if err := os.Chmod(hooks, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(hooks, 0o755) })

	applier := Applier{Paths: fixture.paths, Machine: fixture.machine, Runner: fixture.runner, Log: Logger{Out: io.Discard}}
	_, err := applier.applyRepositoryHookTargets(true)
	if err == nil {
		t.Fatal("an unwritable hooks directory did not stop the apply")
	}
	if !strings.Contains(err.Error(), "write hook ownership") {
		t.Fatalf("the apply wrote before it claimed: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(hooks, "post-checkout")); !os.IsNotExist(statErr) {
		t.Fatal("a hook was installed with no ownership record")
	}
}
