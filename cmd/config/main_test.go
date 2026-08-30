package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildConfig compiles the command so a test drives the real argv boundary
// rather than a rearranged copy of it.
func buildConfig(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "config")
	build := exec.Command("go", "build", "-o", binary, ".")
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
