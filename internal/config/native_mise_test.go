package config

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type nativeStatusRunner struct {
	result      Result
	calls       []string
	configFiles string
}

func (r *nativeStatusRunner) Exists(string) bool { return true }
func (r *nativeStatusRunner) Run(_ context.Context, _ string, args ...string) Result {
	r.calls = append(r.calls, strings.Join(args, " "))
	if slices.Equal(args, []string{"--version"}) {
		return Result{Stdout: releaseMiseVersion}
	}
	if slices.Equal(args, []string{"config", "ls", "--json"}) {
		files := r.configFiles
		if files == "" {
			files = "[]"
		}
		return Result{Stdout: files}
	}
	return r.result
}

func nativeFixture(t *testing.T) (NativeMise, *nativeStatusRunner) {
	t.Helper()
	paths := testPaths(t)
	if err := os.Mkdir(paths.InRoot("mise"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureMiseConfigBinding(paths); err != nil {
		t.Fatal(err)
	}
	runner := &nativeStatusRunner{}
	native := NewNativeMise(paths, io.Discard)
	native.Probe = runner
	return native, runner
}

func TestNativeMachineHealthIncludesMissingAppsRepositoriesAndMerges(t *testing.T) {
	native, runner := nativeFixture(t)
	runner.result = Result{Err: errors.New("native state differs"), Stdout: `{
	  "packages":{"brew-cask":{"available":true,"packages":[{"package":"example-app","state":"missing"}]}},
	  "repos":[{"path":"~/projects/example","state":"differs"}],
	  "dotfiles":{"edits":[{"path":"~/.agent.json","state":"conflict"}]},
	  "macos_login_items":{"entries":[{"name":"Example helper","state":"missing"}]},
	  "tools":[{"tool":"node","state":"installed"}]
	}`}
	resource := native.Inspect()
	if resource.State != Drift || resource.Failed() == 0 {
		t.Fatalf("missing native resources read as healthy: %+v", resource)
	}
	for _, want := range []string{"example-app", "~/projects/example", "~/.agent.json", "Example helper"} {
		if !strings.Contains(strings.Join(resource.Details, "\n"), want) {
			t.Fatalf("missing %s in %+v", want, resource)
		}
	}
	if !slices.Contains(runner.calls, "bootstrap status --json --missing") {
		t.Fatalf("Config did not use aggregate native health: %v", runner.calls)
	}
	if err := (Report{Resources: []Resource{resource}}).PreflightError(); err != nil {
		t.Fatalf("live machine drift blocked an independent snapshot: %v", err)
	}
}

func TestUnreadableNativeStatusCannotLookHealthy(t *testing.T) {
	for _, output := range []string{"", "{}", "not-json", `{"unexpected":true}`, `{"tools":null}`} {
		native, runner := nativeFixture(t)
		runner.result = Result{Stdout: output}
		if resource := native.Inspect(); resource.State != Unavailable || resource.Failed() == 0 {
			t.Fatalf("unreadable status %q = %+v", output, resource)
		}
	}
}

func TestUnavailableNativeProvidersCannotLookHealthy(t *testing.T) {
	native, runner := nativeFixture(t)
	runner.result = Result{Stdout: `{"packages":{"brew":{"available":false,"packages":["example"]}}}`}
	if resource := native.Inspect(); resource.State != Unavailable || resource.Failed() == 0 {
		t.Fatalf("unavailable provider read as healthy: %+v", resource)
	}
	runner.result = Result{Stdout: `{"dotfiles":{"files":[{"path":"~/.client.json","state":"tracked"}]}}`}
	if resource := native.Inspect(); resource.State != Current || len(resource.Details) != 0 {
		t.Fatalf("native tracking read as drift: %+v", resource)
	}
}

func TestNativePlanBindsIncludedConfigurationAndPreservesAuthoredLinks(t *testing.T) {
	native, runner := nativeFixture(t)
	gitTest(t, native.Paths.Root, "init", "--quiet")
	include := filepath.Join(t.TempDir(), "extra.toml")
	if err := os.WriteFile(include, []byte("[tools]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal([]map[string]string{{"path": include}})
	runner.configFiles = string(data)
	// A directory link is authored configuration, not a directory to crawl.
	if err := os.Symlink(t.TempDir(), native.Paths.InRoot("linked-source")); err != nil {
		t.Fatal(err)
	}
	first, err := native.configurationDigest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(include, []byte("[tools]\nnode = '24'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := native.configurationDigest(context.Background())
	if err != nil || first == second {
		t.Fatalf("included configuration was not bound to confirmation: %v", err)
	}
	runner.configFiles = "null"
	if _, err := native.configurationDigest(context.Background()); err == nil {
		t.Fatal("an unreadable effective configuration was accepted")
	}
}

func TestNativeRepairPreservesForeignGlobalConfiguration(t *testing.T) {
	native, _ := nativeFixture(t)
	if err := os.Remove(miseConfigDir(native.Paths)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(miseConfigDir(native.Paths), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(miseConfigDir(native.Paths), "config.toml")
	if err := os.WriteFile(foreign, []byte("# private configuration\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	native.Install = func() error { t.Fatal("repair installed before checking the binding"); return nil }
	if plan := native.plan(context.Background(), UpdateRepair); plan[0].State != UpdateUnavailable {
		t.Fatalf("conflicting global configuration can be overwritten: %+v", plan)
	}
	if err := native.Apply(UpdateRepair); err == nil {
		t.Fatal("repair accepted a conflicting configuration")
	}
	if data, err := os.ReadFile(foreign); err != nil || string(data) != "# private configuration\n" {
		t.Fatalf("foreign configuration changed: %q, %v", data, err)
	}
}

func TestNativePreviewStripsTerminalControlSequences(t *testing.T) {
	native, runner := nativeFixture(t)
	runner.result = Result{Stdout: "\x1b[2J\x1b[31mupgrade example-app\x1b[0m\x07\n"}
	groups := native.plan(context.Background(), UpdateSoftware)
	if got := strings.Join(groups[0].Details, "\n"); got != "upgrade example-app" {
		t.Fatalf("provider control bytes reached the review: %q", got)
	}
}

func TestNativeRepositoryUpdateAndMergeWithMise(t *testing.T) {
	mise, err := exec.LookPath("mise")
	if err != nil {
		t.Fatal("the native integration proof requires mise:", err)
	}
	paths := writeMachineTOML(t, `kind = "azohra.config.machine"
schema = 8
[repository]
branch = "main"
url = "https://example.com/machine.git"
`)
	gitTest(t, paths.Root, "init", "--quiet")
	if err := os.MkdirAll(paths.InRoot("mise"), 0o755); err != nil {
		t.Fatal(err)
	}
	remote := repositoryFixtureAt(t, filepath.Join(t.TempDir(), "source.git"), "first\n")
	nativeConfig := `[settings]
experimental = true
[bootstrap.repos]
"~/projects/example" = { url = "` + remote + `", ref = "main" }
[dotfiles]
"~/.example-source" = "~/projects/example/config.toml"
"~/.client.json/shared" = { source = "shared.json", merge = true }
`
	if err := os.WriteFile(paths.InRoot("mise", "config.toml"), []byte(nativeConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	shared := paths.InRoot("mise", "shared.json")
	if err := os.WriteFile(shared, []byte(`{"managed":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	client := paths.InHome(".client.json")
	if err := os.WriteFile(client, []byte(`{"foreign":"keep"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureMiseConfigBinding(paths); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	native := NewNativeMise(paths, &output)
	probe := native.Probe.(OSRunner)
	probe.Executables = map[string]string{"mise": mise}
	isolation := []string{
		"HOME=" + paths.Home,
		"XDG_CONFIG_HOME=" + paths.InHome(".config"),
		"XDG_DATA_HOME=" + paths.InHome(".local", "share"),
		"XDG_STATE_HOME=" + paths.InHome(".local", "state"),
		"XDG_CACHE_HOME=" + paths.InHome(".cache"),
		"MISE_DATA_DIR=" + paths.InHome(".local", "share", "mise"),
		"MISE_STATE_DIR=" + paths.InHome(".local", "state", "mise"),
		"MISE_CACHE_DIR=" + paths.InHome(".cache", "mise"),
		"MISE_SYSTEM_CONFIG_FILE=" + paths.InHome("unused-system.toml"),
		"MISE_TRUSTED_CONFIG_PATHS=" + paths.Root,
		"MISE_YES=1",
	}
	probe.Environment = append(probe.Environment, isolation...)
	native.Probe = probe
	native.Live.Environment, native.Live.Executables = probe.Environment, probe.Executables
	if err := native.Bootstrap(func() error { return nil }); err != nil {
		t.Fatalf("native fixture bootstrap: %v\n%s", err, output.String())
	}
	if resource := native.Inspect(); resource.State != Current {
		t.Fatalf("native fixture did not converge: %+v\n%s", resource, output.String())
	}
	work := t.TempDir()
	gitTest(t, work, "clone", "--quiet", remote, ".")
	gitTest(t, work, "config", "user.name", "Config Test")
	gitTest(t, work, "config", "user.email", "config@example.invalid")
	gitTest(t, work, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(work, "config.toml"), []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "commit", "--quiet", "-am", "Update source")
	gitTest(t, work, "push", "--quiet")
	if err := os.WriteFile(shared, []byte(`{"managed":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	updater := NewUpdater(paths, &output, "dev")
	updater.Native = &native
	plan, err := updater.Plan(UpdateRepositories)
	if err != nil || plan.Blocked || !plan.HasWork() {
		t.Fatalf("native repository preview: %+v, %v\n%s", plan, err, output.String())
	}
	if data, _ := os.ReadFile(paths.InHome(".example-source")); string(data) != "first\n" {
		t.Fatal("preview changed the repository")
	}
	// A confirmed plan must bind declarations even if its displayed native
	// commands would look unchanged after a source edit.
	if err := os.WriteFile(shared, []byte(`{"managed":3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := updater.Apply(plan); err == nil || !strings.Contains(err.Error(), "configuration changed") {
		t.Fatalf("changed declarations were applied: %v", err)
	}
	plan, err = updater.Plan(UpdateRepositories)
	if err != nil {
		t.Fatal(err)
	}
	if err := updater.Apply(plan); err != nil {
		t.Fatalf("native repository update: %v\n%s", err, output.String())
	}
	if data, _ := os.ReadFile(paths.InHome(".example-source")); string(data) != "second\n" {
		t.Fatalf("native repository was not updated: %s", data)
	}
	var merged map[string]any
	data, _ := os.ReadFile(client)
	if err := json.Unmarshal(data, &merged); err != nil || merged["foreign"] != "keep" || merged["managed"] != float64(3) {
		t.Fatalf("native merge lost client state: %s (%v)", data, err)
	}
	if err := os.WriteFile(paths.InHome("projects", "example", "config.toml"), []byte("local edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err = updater.Plan(UpdateRepositories)
	if err != nil || plan.Blocked {
		t.Fatalf("dirty repository preview: %+v, %v", plan, err)
	}
	if err := updater.Apply(plan); err != nil {
		t.Fatalf("dirty repository was not safely skipped: %v\n%s", err, output.String())
	}
	if data, _ := os.ReadFile(paths.InHome(".example-source")); string(data) != "local edit\n" {
		t.Fatal("native update overwrote a local repository edit")
	}
}

func TestNativeMaintenancePreviewsAndAppliesTheSameScope(t *testing.T) {
	native, runner := nativeFixture(t)
	runner.result = Result{Stdout: "upgrade example-app to 2.0\n"}
	groups := native.plan(context.Background(), UpdateSoftware)
	if len(groups) != 2 || groups[0].State != UpdatePending {
		t.Fatalf("native software preview = %+v", groups)
	}
	for _, command := range runner.calls {
		if command != "--version" && !strings.Contains(command, "--dry-run") {
			t.Fatalf("preview executed a mutation: %s", command)
		}
	}
	log := filepath.Join(t.TempDir(), "commands")
	t.Setenv("NATIVE_MISE_TEST_LOG", log)
	writeUpdateExecutable(t, native.Paths.InHome(".local", "bin", "mise"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$NATIVE_MISE_TEST_LOG\"\n")
	if err := native.Apply(UpdateSoftware); err != nil {
		t.Fatal(err)
	}
	commands, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if string(commands) != "upgrade --global --no-prune --yes\nbootstrap packages upgrade --yes\n" {
		t.Fatalf("software scope = %s", commands)
	}
	if err := native.Apply(UpdateRepositories); err != nil {
		t.Fatal(err)
	}
	commands, _ = os.ReadFile(log)
	if !strings.Contains(string(commands), "bootstrap repos update --skip-dirty --yes\ndotfiles apply --yes\n") {
		t.Fatalf("repository scope lost linked resources: %s", commands)
	}
}

func TestNativeCommandsIgnoreTheCallingProjectsMiseContext(t *testing.T) {
	paths := testPaths(t)
	t.Setenv("MISE_ENV", "project")
	t.Setenv("MISE_NODE_VERSION", "99")
	t.Setenv("MISE_CONFIG_DIR", t.TempDir())
	native := NewNativeMise(paths, io.Discard)
	probe := native.Probe.(OSRunner)
	environment := childEnvironment(probe.Environment, probe.Unset)
	if environmentValue(environment, "MISE_ENV") != "" || environmentValue(environment, "MISE_NODE_VERSION") != "" {
		t.Fatalf("project context reached native machine commands: %v", probe.Unset)
	}
	if probe.Dir != paths.Home || environmentValue(environment, "MISE_CONFIG_DIR") != miseConfigDir(paths) {
		t.Fatalf("wrong native machine context: %+v", probe)
	}
}
