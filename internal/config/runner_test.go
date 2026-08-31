package config

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// hostileMiseSelectors each change which document mise loads, verified
// against the real binary: a global config file replaces the machine
// document, a system config file is loaded alongside it, and an ignored path
// erases it. Naming the configuration directory does not settle any of them.
var hostileMiseSelectors = []string{
	"MISE_GLOBAL_CONFIG_FILE",
	"MISE_SYSTEM_CONFIG_FILE",
	"MISE_IGNORED_CONFIG_PATHS",
}

func TestMiseChildIgnoresAnAmbientConfigurationSelection(t *testing.T) {
	paths := testPaths(t)
	for _, name := range hostileMiseSelectors {
		t.Setenv(name, "/tmp/somebody-elses.toml")
	}
	environment := ChildEnvironment(MiseEnvironment(paths))
	for _, name := range hostileMiseSelectors {
		var selections []string
		for _, entry := range environment {
			if value, ok := strings.CutPrefix(entry, name+"="); ok {
				selections = append(selections, value)
			}
		}
		if len(selections) != 1 {
			t.Errorf("%s appears %d times in the child environment: %v", name, len(selections), selections)
			continue
		}
		if selections[0] != "" {
			t.Errorf("%s reached the mise child as %q", name, selections[0])
		}
	}
	// The scoping Config does set still has to survive the overlay.
	if !slices.Contains(environment, "MISE_CONFIG_DIR="+paths.InRoot("mise")) {
		t.Error("the child lost Config's own configuration directory")
	}
}

func TestReleaseRunnerPinsEveryProvenanceKnob(t *testing.T) {
	// An ambient value turns each of these off, and this runner exists to
	// acquire a verified release.
	pinned := map[string]string{
		"MISE_GITHUB_GITHUB_ATTESTATIONS":    "true",
		"MISE_GITHUB_SLSA":                   "true",
		"MISE_PROVENANCE_API_FAILURES_FATAL": "true",
	}
	for name := range pinned {
		t.Setenv(name, "false")
	}
	updater := Updater{Substrate: NewLiveRunner(t.TempDir())}
	environment := ChildEnvironment(updater.releaseRunner().Environment)
	for name, want := range pinned {
		var values []string
		for _, entry := range environment {
			if value, ok := strings.CutPrefix(entry, name+"="); ok {
				values = append(values, value)
			}
		}
		if len(values) != 1 || values[0] != want {
			t.Errorf("%s reached the release child as %v, want [%s]", name, values, want)
		}
	}
}

func TestMachineRunnersUseOnlyTheCanonicalMise(t *testing.T) {
	paths := testPaths(t)
	canonical := misePath(paths)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical, []byte("#!/bin/sh\nprintf canonical"), 0o755); err != nil {
		t.Fatal(err)
	}
	fallback := t.TempDir()
	if err := os.WriteFile(filepath.Join(fallback, "mise"), []byte("#!/bin/sh\nprintf wrong"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fallback+string(os.PathListSeparator)+os.Getenv("PATH"))

	runner := NewMachineRunner(paths)
	result := runner.Run(context.Background(), "mise")
	if result.Err != nil || result.Stdout != "canonical" {
		t.Fatalf("machine runner = %+v", result)
	}

	var output bytes.Buffer
	live := NewMachineLiveRunner(paths)
	live.Stdout, live.Stderr = &output, &output
	if err := live.Command("mise"); err != nil {
		t.Fatal(err)
	}
	if output.String() != "canonical" {
		t.Fatalf("machine live runner used %q", output.String())
	}
}

// A buffered command's stderr is the only account of why it failed. Losing it
// leaves the reader with "exit status 1" and nothing to act on.
func TestFailureReportsTheCommandsOwnWords(t *testing.T) {
	exited := exec.Command("/usr/bin/false").Run()
	tests := []struct {
		name   string
		result Result
		want   string
	}{
		{"stderr leads", Result{Err: exited, Stderr: "defaults: unknown option\n"}, "defaults: unknown option (exit status 1)"},
		{"blank lines skipped", Result{Err: exited, Stderr: "\n\n  defaults: domain not found\n"}, "defaults: domain not found (exit status 1)"},
		{"silent failure keeps the status", Result{Err: exited}, "exit status 1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.result.Failure(); got == nil || got.Error() != test.want {
				t.Fatalf("Failure() = %v, want %q", got, test.want)
			}
		})
	}
	if err := (Result{Stdout: "fine"}).Failure(); err != nil {
		t.Fatalf("a successful command reported %v", err)
	}
}
