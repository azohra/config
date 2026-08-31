package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

// fixtureHome is a machine with a valid document and no mise, so exactly one
// check fails and the report is the same on every machine that runs this.
func fixtureHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, "Library", "Application Support", "Config", "repository")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	document := `kind = "azohra.config.machine"
schema = 1

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
	command.Env = append(os.Environ(), "HOME="+home)
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

	if explicitCode != 1 {
		t.Fatalf("config --status on a failing machine exited %d:\n%s", explicitCode, explicit)
	}
	if implicitCode != explicitCode {
		t.Fatalf("config without a terminal exited %d, but --status exited %d:\n%s", implicitCode, explicitCode, implicit)
	}
	// Both write the same failure line to stderr; the reports either side of
	// it are what has to match.
	withoutVerdict := func(output string) string {
		return strings.ReplaceAll(output, "error: configuration needs attention\n", "")
	}
	if withoutVerdict(explicit) != withoutVerdict(implicit) {
		t.Fatalf("the two status surfaces disagree:\n--status:\n%s\nbare:\n%s", explicit, implicit)
	}
	if !strings.Contains(implicit, "error: configuration needs attention") {
		t.Fatalf("config without a terminal did not say why it failed:\n%s", implicit)
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
	if code != 1 || !strings.Contains(withMessage, "invalid snapshot request") {
		t.Fatalf("config --snapshot accepted a message (exit %d):\n%s", code, withMessage)
	}

	withoutMessage, code := runConfig(t, binary, home, "--snapshot")
	if code != 1 || strings.Contains(withoutMessage, "invalid snapshot request") {
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

	output, code = runConfig(t, binary, home, "install")
	if code != 0 || output != "" {
		t.Fatalf("config install = %q (exit %d)", output, code)
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
	root := filepath.Join(home, "Library", "Application Support", "Config", "repository")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte("newer-machine-schema"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"update"}, {"update", "software"}, {"update", "repositories"}} {
		output, code := runConfig(t, binary, home, args...)
		if code != 1 || !strings.Contains(output, "mise unavailable") || strings.Contains(output, "config.toml") {
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

func TestBootstrapInstallsTheCommandBeforeARestoreFailure(t *testing.T) {
	binary, home := buildConfig(t), t.TempDir()
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
schema = 1

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
	if code != 1 || !strings.Contains(output, "mise unavailable") {
		t.Fatalf("bootstrap without mise = %q (exit %d)", output, code)
	}
	installed := filepath.Join(home, ".local", "bin", "config")
	if info, err := os.Stat(installed); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("bootstrap did not leave a permanent command: %v, %v", info, err)
	}
}
