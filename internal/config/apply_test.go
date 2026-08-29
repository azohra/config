package config

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Every step the Logger writes must read back as one: a reader that colors or
// filters this output depends on the writer and StepGlyph agreeing.
func TestStepGlyphRecognizesEveryLoggerStep(t *testing.T) {
	var out bytes.Buffer
	log := Logger{Out: &out}
	log.OK("pushed")
	log.Info("validating")
	log.Warn("commit remains local")
	log.Error("push rejected")

	for _, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		glyph, ok := StepGlyph(line)
		if !ok {
			t.Fatalf("StepGlyph did not recognize the Logger's own %q", line)
		}
		if !strings.HasPrefix(strings.TrimLeft(line, " "), glyph+" ") {
			t.Fatalf("StepGlyph(%q) = %q, which the line does not lead with", line, glyph)
		}
	}

	var section bytes.Buffer
	Logger{Out: &section}.Section("Snapshot")
	notSteps := append(strings.Split(section.String(), "\n"),
		"[check] ~/.gitconfig  symlink  applied", " 1 file changed", "  1 file changed", "  ✓", "✓ unindented", "  ↔ no Logger writes this")
	for _, line := range notSteps {
		if glyph, ok := StepGlyph(line); ok {
			t.Fatalf("StepGlyph(%q) claimed glyph %q", line, glyph)
		}
	}
}

// converged answers every miseFacts probe as already-correct, so applyMise
// reaches its one live command and stops: nothing to fix, no restarts.
type converged struct{}

func (converged) Run(_ context.Context, name string, args ...string) Result {
	switch {
	case name == "defaults" && slices.Contains(args, "com.apple.mouse.tapBehavior"):
		return Result{Stdout: "1\n"}
	case name == "plutil":
		return Result{Stdout: "0\n"}
	case name == "hidutil":
		return Result{Stdout: "()\n"}
	}
	return Result{}
}

func (converged) Exists(string) bool { return true }

// One dirty checkout must not block the rest of machine reconciliation.
func TestADirtyCheckoutDoesNotBlockApply(t *testing.T) {
	fakeBin := t.TempDir()
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	commandLog := filepath.Join(t.TempDir(), "commands")
	t.Setenv("COMMAND_LOG", commandLog)
	mise := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$COMMAND_LOG\"\nfor arg in \"$@\"; do [ \"$arg\" = --skip-dirty ] && exit 0; done\n" +
		"echo 'repos: ~/Projects/example has local changes' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "mise"), []byte(mise), 0o755); err != nil {
		t.Fatal(err)
	}

	var chatter bytes.Buffer
	applier := Applier{
		Paths:   testPaths(t),
		Machine: testMachine(),
		Runner:  converged{},
		Live:    LiveRunner{Stdout: &chatter, Stderr: &chatter},
		Log:     Logger{Out: &chatter},
	}
	if err := applier.applyMise(); err != nil {
		t.Fatalf("a dirty checkout blocked apply: %v", err)
	}
	commands, err := os.ReadFile(commandLog)
	if err != nil {
		t.Fatal(err)
	}
	want := "bootstrap --yes --skip-dirty\n"
	if string(commands) != want {
		t.Fatalf("mise order = %q, want %q", commands, want)
	}
}
