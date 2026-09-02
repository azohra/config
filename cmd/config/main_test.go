package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azohra/config/internal/config"
)

// buildConfig compiles the command so a test drives the real argv boundary
// rather than a rearranged copy of it.
func buildConfig(t *testing.T) string {
	return buildConfigVersion(t, "")
}

func buildConfigVersion(t *testing.T, version string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "config")
	args := []string{"build", "-o", binary}
	if version != "" {
		args = append(args, "-ldflags", "-X main.version="+version)
	}
	build := exec.Command("go", append(args, ".")...)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build config: %v\n%s", err, output)
	}
	return binary
}

// fixtureHome is a machine with a valid document and an empty, readable Dock,
// so the report is the same on every platform that runs this.
func fixtureHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	defaults := `#!/bin/sh
printf '%s\n' '<?xml version="1.0"?><plist version="1.0"><dict><key>persistent-apps</key><array/></dict></plist>'
`
	if err := os.WriteFile(filepath.Join(bin, "defaults"), []byte(defaults), 0o755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "Library", "Application Support", "Config", "repository")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	document := `kind = "azohra.config.machine"
schema = 4
dock = true

[repository]
branch = "main"
url = "https://github.com/example/machine.git"
`
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte(document), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", root, "init", "--quiet", "--initial-branch=main").Run(); err != nil {
		t.Fatal(err)
	}
	return home
}

func runConfig(t *testing.T, binary, home string, args ...string) (string, int) {
	t.Helper()
	command := exec.Command(binary, args...)
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		if name != "HOME" && name != "PATH" {
			command.Env = append(command.Env, value)
		}
	}
	command.Env = append(command.Env,
		"HOME="+home,
		"PATH="+filepath.Join(home, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	output, err := command.CombinedOutput()
	code := 0
	var exit *exec.ExitError
	if err != nil {
		if !asExit(err, &exit) {
			t.Fatalf("run config %v: %v\n%s", args, err, output)
		}
		code = exit.ExitCode()
	}
	return string(output), code
}

func asExit(err error, target **exec.ExitError) bool {
	exit, ok := err.(*exec.ExitError)
	if ok {
		*target = exit
	}
	return ok
}

// Config prints the same report whether it was asked for status or simply
// handed no terminal, so it owes the same answer about the machine either
// way. Anything running Config from a script reads the exit code.
func TestStatusReportsTheSameVerdictWithoutATerminal(t *testing.T) {
	binary, home := buildConfig(t), fixtureHome(t)

	explicit, explicitCode := runConfig(t, binary, home, "--status")
	implicit, implicitCode := runConfig(t, binary, home)

	if explicitCode != 0 {
		t.Fatalf("config --status on an inspectable machine exited %d:\n%s", explicitCode, explicit)
	}
	if implicitCode != explicitCode {
		t.Fatalf("config without a terminal exited %d, but --status exited %d:\n%s", implicitCode, explicitCode, implicit)
	}
	if explicit != implicit {
		t.Fatalf("the two status surfaces disagree:\n--status:\n%s\nbare:\n%s", explicit, implicit)
	}
	if strings.Contains(implicit, "error:") {
		t.Fatalf("config without a terminal invented a failure:\n%s", implicit)
	}
}

// A character device is not a terminal. /dev/null is one, so the old
// ModeCharDevice test called it a terminal and `config > /dev/null` started
// the interactive interface against a stream nobody could read.
func TestDevNullIsNotATerminal(t *testing.T) {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if terminal(null) {
		t.Fatal("/dev/null was treated as a terminal")
	}

	// A regular file is not one either, and a test binary's own streams are
	// pipes, so nothing here should read as interactive.
	regular, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	if terminal(regular) {
		t.Fatal("a regular file was treated as a terminal")
	}
}

func TestSnapshotTakesNoMessage(t *testing.T) {
	binary, home := buildConfig(t), fixtureHome(t)

	withMessage, code := runConfig(t, binary, home, "--snapshot", "Manual subject")
	if code != 1 || !strings.Contains(withMessage, "usage: config --snapshot") {
		t.Fatalf("config --snapshot accepted a message (exit %d):\n%s", code, withMessage)
	}

	// The parameterless form is the real one; it fails here only because this
	// home has no managed checkout, which is a different refusal.
	withoutMessage, code := runConfig(t, binary, home, "--snapshot")
	if code != 1 || strings.Contains(withoutMessage, "usage:") {
		t.Fatalf("config --snapshot rejected its parameterless form (exit %d):\n%s", code, withoutMessage)
	}
}

func TestPathAndInstallDoNotRequireAMachineRepository(t *testing.T) {
	binary, home := buildConfig(t), t.TempDir()

	output, code := runConfig(t, binary, home, "path")
	wantPath := filepath.Join(home, "Library", "Application Support", "Config", "repository") + "\n"
	if code != 0 || output != wantPath {
		t.Fatalf("config path = %q (exit %d), want %q", output, code, wantPath)
	}

	// The test binary carries no release version, and installing one as the
	// canonical command has to say so: config update skips the release
	// transition for a development build, so it would otherwise look current
	// forever.
	output, code = runConfig(t, binary, home, "install")
	if code != 0 {
		t.Fatalf("config install = %q (exit %d)", output, code)
	}
	if !strings.Contains(output, "development build") {
		t.Fatalf("config install said nothing about the build it installed: %q", output)
	}
	installed := filepath.Join(home, ".local", "bin", "config")
	installedBytes, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	binaryBytes, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(installed)
	if err != nil {
		t.Fatal(err)
	}
	if string(installedBytes) != string(binaryBytes) || info.Mode().Perm() != 0o755 {
		t.Fatalf("installed command differs from the running binary or is not executable")
	}
}

func TestUpdateRunsBeforeReadingTheMachineDocument(t *testing.T) {
	binary, home := buildConfigVersion(t, "v0.4.0"), t.TempDir()
	writeMainTestReleaseMise(t, home, `#!/bin/sh
if [ "$1" = --version ]; then
  printf '2026.9.1\n'
  exit 0
fi
exit 1
`)
	root := filepath.Join(home, "Library", "Application Support", "Config", "repository")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte("newer-machine-schema"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"update"}, {"update", "software"}, {"update", "repositories"}} {
		output, code := runConfig(t, binary, home, args...)
		if code != 1 || !strings.Contains(output, "Config:") || strings.Contains(output, "config.toml") {
			t.Fatalf("config %s read the machine document before updating itself (exit %d):\n%s", strings.Join(args, " "), code, output)
		}
	}
}

func TestUpdateRejectsAnUnknownScope(t *testing.T) {
	output, code := runConfig(t, buildConfig(t), t.TempDir(), "update", "everything")
	if code != 1 || !strings.Contains(output, "usage: config update [software | repositories]") {
		t.Fatalf("unknown update scope exited %d:\n%s", code, output)
	}
}

func TestUpdateOptionsRequireOneExplicitApplyMode(t *testing.T) {
	for _, test := range []struct {
		args []string
		want updateOptions
		ok   bool
	}{
		{nil, updateOptions{scope: config.UpdateAll}, true},
		{[]string{"software", "--dry-run"}, updateOptions{scope: config.UpdateSoftware, dryRun: true}, true},
		{[]string{"--yes", "repositories"}, updateOptions{scope: config.UpdateRepositories, yes: true}, true},
		{[]string{"--dry-run", "--yes"}, updateOptions{}, false},
		{[]string{"software", "repositories"}, updateOptions{}, false},
		{[]string{"--force"}, updateOptions{}, false},
	} {
		got, err := parseUpdateOptions(test.args)
		if (err == nil) != test.ok || got != test.want {
			t.Errorf("parseUpdateOptions(%q) = %+v, %v", test.args, got, err)
		}
	}
}

func TestRedirectedUpdatePreviewsWithoutChangingTheMac(t *testing.T) {
	binary, home := buildConfig(t), fixtureHome(t)
	root := filepath.Join(home, "Library", "Application Support", "Config", "repository")
	document, err := os.ReadFile(filepath.Join(root, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	document = bytes.Replace(document, []byte("dock = true"), []byte("dock = true\nmise = true"), 1)
	if err := os.WriteFile(filepath.Join(root, "config.toml"), document, 0o644); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "mise-commands")
	t.Setenv("UPDATE_TEST_LOG", log)
	mise := filepath.Join(home, ".local", "bin", "mise")
	if err := os.MkdirAll(filepath.Dir(mise), 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$UPDATE_TEST_LOG"
if [ "$1" = --version ]; then printf '2026.9.1\n'; fi
if [ "$1" = outdated ]; then printf '{}\n'; fi
if [ "$1 $2 $3" = "bootstrap packages status" ]; then printf '{"brew":{"packages":[{}]}}\n'; fi
`
	if err := os.WriteFile(mise, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	output, code := runConfig(t, binary, home, "update", "software")
	if code != 0 || !strings.Contains(output, "Update plan · software") || !strings.Contains(output, "No changes made; run config update software --yes") {
		t.Fatalf("redirected update exited %d:\n%s", code, output)
	}
	commands, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(commands), "upgrade --yes") || strings.Contains(string(commands), "packages upgrade") {
		t.Fatalf("preview mutated through Mise:\n%s", commands)
	}
}

func TestHelpKeepsInstallAsDistributionPlumbing(t *testing.T) {
	output, code := runConfig(t, buildConfig(t), t.TempDir(), "help")
	if code != 0 {
		t.Fatalf("config help exited %d:\n%s", code, output)
	}
	if strings.Contains(output, "\n  config install\n") {
		t.Fatalf("config help exposes the release handoff command:\n%s", output)
	}
}

func TestPruneRequiresAnExplicitApplyChoice(t *testing.T) {
	for _, test := range []struct {
		args []string
		want pruneOptions
		ok   bool
	}{
		{nil, pruneOptions{}, true},
		{[]string{"--dry-run"}, pruneOptions{dryRun: true}, true},
		{[]string{"--yes"}, pruneOptions{yes: true}, true},
		{[]string{"--yes", "--dry-run"}, pruneOptions{}, false},
		{[]string{"--force"}, pruneOptions{}, false},
	} {
		got, err := parsePruneOptions(test.args)
		if (err == nil) != test.ok || got != test.want {
			t.Errorf("parsePruneOptions(%v) = %+v, %v; want %+v, ok %v", test.args, got, err, test.want, test.ok)
		}
	}

	for _, answer := range []string{"y\n", "YES\n"} {
		var output bytes.Buffer
		confirmed, err := confirmPrune(strings.NewReader(answer), &output)
		if err != nil || !confirmed || !strings.Contains(output.String(), "[y/N]") {
			t.Fatalf("confirmPrune(%q) = %v, %v, %q", answer, confirmed, err, output.String())
		}
	}
	for _, answer := range []string{"\n", "no\n", "anything else\n"} {
		confirmed, err := confirmPrune(strings.NewReader(answer), &bytes.Buffer{})
		if err != nil || confirmed {
			t.Fatalf("confirmPrune(%q) = %v, %v", answer, confirmed, err)
		}
	}
}

func TestBootstrapInstallsTheCommandBeforeAResourceFailure(t *testing.T) {
	binary, home := buildConfig(t), t.TempDir()
	writeMainTestMise(t, home, `#!/bin/sh
if [ "$1" = --version ]; then
  printf '2026.9.1\n'
  exit 0
fi
exit 1
`)
	source := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", source}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
	git("init", "--quiet", "--initial-branch=main")
	git("config", "user.name", "Config Test")
	git("config", "user.email", "config@example.invalid")
	document := `kind = "azohra.config.machine"
schema = 4
mise = true

[repository]
branch = "main"
url = "` + source + `"
`
	if err := os.WriteFile(filepath.Join(source, "config.toml"), []byte(document), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "config.toml")
	git("commit", "--quiet", "-m", "Add machine contract")

	output, code := runConfig(t, binary, home, "bootstrap", source)
	if code != 1 || !strings.Contains(output, "Mise") {
		t.Fatalf("bootstrap with a failed resource = %q (exit %d)", output, code)
	}
	installed := filepath.Join(home, ".local", "bin", "config")
	if info, err := os.Stat(installed); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("bootstrap did not leave a permanent command: %v, %v", info, err)
	}
}

func writeMainTestMise(t *testing.T, home, script string) {
	t.Helper()
	path := filepath.Join(home, ".local", "bin", "mise")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeMainTestReleaseMise(t *testing.T, home, script string) {
	t.Helper()
	path := filepath.Join(home, ".cache", "config", "release", "mise")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestArgumentErrorsDoNotWaitForTheMachineDocument(t *testing.T) {
	// A Mac with no managed checkout reported a typo as a missing repository
	// and prescribed bootstrap, which is not the problem.
	binary, home := buildConfig(t), t.TempDir()
	for _, invocation := range [][]string{
		{"--stats"},
		{"snapshot"},
		{"--status", "extra"},
		{"--apply"},
		{"bootstrap"},
		{"update", "everything"},
	} {
		output, code := runConfig(t, binary, home, invocation...)
		if code != 1 {
			t.Errorf("config %s exited %d:\n%s", strings.Join(invocation, " "), code, output)
			continue
		}
		if strings.Contains(output, "bootstrap <repository>") && invocation[0] != "bootstrap" {
			t.Errorf("config %s was reported as a missing repository:\n%s", strings.Join(invocation, " "), output)
		}
		if !strings.Contains(output, "usage:") && !strings.Contains(output, "unknown") {
			t.Errorf("config %s did not name an argument problem:\n%s", strings.Join(invocation, " "), output)
		}
	}
}

func TestApplyRefusesAPlanTheCurrentStateDoesNotAllow(t *testing.T) {
	// Selection validation against a fresh report is the only place a stale or
	// forged plan is refused, and nothing drove it.
	binary, home := buildConfig(t), fixtureHome(t)

	// A resource the report does not carry at all.
	unknown, code := runConfig(t, binary, home, "--apply", `[{"id":"not-a-resource","action":"apply"}]`)
	if code != 1 || !strings.Contains(unknown, `unknown resource "not-a-resource"`) {
		t.Fatalf("config --apply accepted an unknown resource (exit %d):\n%s", code, unknown)
	}

	// A real resource, and an action its current state does not allow.
	stale, code := runConfig(t, binary, home, "--apply", `[{"id":"dock","action":"apply"}]`)
	if code != 1 || !strings.Contains(stale, "no longer allows apply") {
		t.Fatalf("config --apply accepted a stale plan (exit %d):\n%s", code, stale)
	}

	// The same resource twice, which a forged plan can carry and the terminal
	// interface never produces.
	repeated, code := runConfig(t, binary, home, "--apply", `[{"id":"dock","action":"capture"},{"id":"dock","action":"capture"}]`)
	if code != 1 || !strings.Contains(repeated, "appears more than once") {
		t.Fatalf("config --apply accepted a repeated selection (exit %d):\n%s", code, repeated)
	}

	garbage, code := runConfig(t, binary, home, "--apply", "not-a-plan")
	if code != 1 {
		t.Fatalf("config --apply accepted an undecodable plan (exit %d):\n%s", code, garbage)
	}
}

func TestOperationEventModeFramesCommandErrors(t *testing.T) {
	binary, home := buildConfig(t), fixtureHome(t)
	t.Setenv(config.OperationEventsEnv, "1")
	output, code := runConfig(t, binary, home, "--apply", "not-a-plan")
	if code != 1 {
		t.Fatalf("event operation exited %d:\n%s", code, output)
	}
	decoder := json.NewDecoder(strings.NewReader(output))
	var events []config.OperationEvent
	for decoder.More() {
		var event config.OperationEvent
		if err := decoder.Decode(&event); err != nil {
			t.Fatalf("decode event stream: %v\n%s", err, output)
		}
		events = append(events, event)
	}
	if len(events) != 1 || events[0].Kind != config.OperationError || !strings.Contains(events[0].Text, "invalid character") {
		t.Fatalf("events = %#v", events)
	}
}

func TestInternalUpdateRefusesAChangedPlanBeforeApply(t *testing.T) {
	binary, home := buildConfig(t), fixtureHome(t)
	output, code := runConfig(t, binary, home, "--run-update", "software", "sha256:stale")
	if code != 1 || !strings.Contains(output, "update plan changed") {
		t.Fatalf("changed update plan exited %d:\n%s", code, output)
	}
	if strings.Contains(output, "development build") {
		t.Fatalf("changed update plan reached apply:\n%s", output)
	}
}

func TestInstallDoesNotContendForTheCheckoutLock(t *testing.T) {
	// update takes the checkout lock and then runs the acquired release's own
	// install as a child. Installing writes ~/.local/bin/config, not the
	// checkout, so contending for that lock deadlocks an update against
	// itself and no release can ever install its successor.
	binary, home := buildConfig(t), t.TempDir()
	paths, err := config.NewPaths("", home)
	if err != nil {
		t.Fatal(err)
	}
	release, err := config.LockCheckout(paths)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	output, code := runConfig(t, binary, home, "install")
	if code != 0 {
		t.Fatalf("config install refused while the checkout lock was held (exit %d):\n%s", code, output)
	}
	if strings.Contains(output, "already working on") {
		t.Fatalf("config install contended for the checkout lock:\n%s", output)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".local", "bin", "config")); statErr != nil {
		t.Fatalf("install wrote no command: %v", statErr)
	}
}
