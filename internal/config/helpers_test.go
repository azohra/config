package config

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	output, err := exec.Command("git", "rev-parse", "--local-env-vars").Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "list local Git environment variables: %v\n", err)
		os.Exit(1)
	}
	for _, name := range strings.Fields(string(output)) {
		if err := os.Unsetenv(name); err != nil {
			fmt.Fprintf(os.Stderr, "unset %s: %v\n", name, err)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

func testPaths(t *testing.T) Paths {
	t.Helper()
	root := t.TempDir()
	home := t.TempDir()
	return Paths{Root: root, Home: home, StateDir: filepath.Join(t.TempDir(), "state")}
}

func testMachine() Machine {
	tapToClick := true
	return Machine{
		Kind:   MachineKind,
		Schema: MachineSchema,
		Mise:   true,
		Repository: MachineRepository{
			Branch: "main",
			URL:    "https://github.com/example/machine.git",
		},
		Dock:       true,
		ChromePWAs: true,
		MacOS: MachineMacOS{
			CurrentHostTapToClick: &tapToClick,
			ClearUserKeyMapping:   true,
			Spotlight: &SpotlightShortcut{
				ID: 64, Enabled: false, Parameters: []int{32, 49, 1048576}, Type: "standard",
			},
		},
		Preferences: []PreferenceBackup{{
			ID: "example-app", Name: "Example App", Bundle: "com.example.ExampleApp",
			Domain: "com.example.ExampleApp",
		}},
	}
}

// fakeTool is a recording stub on PATH. It records its invocation and exits
// with the given status, so a test can drive both halves of a step's contract.
type fakeTool struct {
	name string
	exit int
}

// fakeTools returns every invocation the LiveRunner drove, in order, so a
// test can prove which commands an apply actually issued.
func fakeTools(t *testing.T, tools ...fakeTool) func() []string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(t.TempDir(), "commands")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("COMMAND_LOG", log)
	for _, tool := range tools {
		// An argument may itself span lines — a property list value does. Fold
		// them so the log stays one line per command.
		script := fmt.Sprintf("#!/bin/sh\nprintf '%%s %%s\\n' %s \"$(printf '%%s' \"$*\" | tr '\\n' ' ')\" >> \"$COMMAND_LOG\"\nexit %d\n", tool.name, tool.exit)
		if err := os.WriteFile(filepath.Join(dir, tool.name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return func() []string {
		data, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}
}

// testApplier builds an Applier over fakes. NewApplier wires the real
// machine; every write path below needs the same shape with the subprocess
// boundary replaced.
func testApplier(t *testing.T, paths Paths, machine Machine, runner Runner) (Applier, *bytes.Buffer) {
	t.Helper()
	var chatter bytes.Buffer
	live := LiveRunner{Dir: paths.Root, Stdout: &chatter, Stderr: &chatter}
	return Applier{
		Paths:    paths,
		Machine:  machine,
		Runner:   runner,
		Live:     live,
		Mise:     runner,
		MiseLive: live,
		Log:      Logger{Out: &chatter},
		Bidir: Bidirectional{
			Paths: paths, Dock: defaultsDockStore{Runner: runner, Live: live},
			Baselines: Baselines{Dir: paths.StateDir},
		},
	}, &chatter
}

func testBidirectional(paths Paths, runner Runner) Bidirectional {
	return Bidirectional{
		Paths: paths, Dock: defaultsDockStore{Runner: runner},
		Baselines: Baselines{Dir: paths.StateDir},
	}
}
