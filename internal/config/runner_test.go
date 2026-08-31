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
	"time"
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

func TestOSRunnerDoesNotWaitOnADescendantThatOutlivesTheCommand(t *testing.T) {
	// exec waits for every descendant that inherited the output pipes, so a
	// probe whose child leaves a background process behind held the 20-second
	// deadline open for as long as that process lived. The command itself
	// succeeded, so the delay is not a failure to report.
	runner := OSRunner{Dir: t.TempDir()}
	start := time.Now()
	result := runner.Run(context.Background(), "/bin/sh", "-c", "sleep 6 & printf started")
	elapsed := time.Since(start)
	if result.Err != nil {
		t.Fatalf("a successful command reported %v", result.Err)
	}
	if result.Stdout != "started" {
		t.Fatalf("stdout = %q", result.Stdout)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Run waited %v on a descendant the command left behind", elapsed)
	}
}

func TestOSRunnerDeadlineOutlastsNoDescendant(t *testing.T) {
	// A cancelled command takes its whole process group with it.
	runner := OSRunner{Dir: t.TempDir()}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	result := runner.Run(ctx, "/bin/sh", "-c", "sleep 30 & sleep 30")
	if result.Err == nil {
		t.Fatal("a cancelled command reported success")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
}

func TestGitProbesAnswerAboutTheRepositoryConfigNamed(t *testing.T) {
	// Every command that proves the managed checkout's identity runs Git. An
	// exported GIT_DIR answers "is this the right repository?" from a
	// different one.
	elsewhere := t.TempDir()
	for _, name := range gitLocalEnvironment {
		t.Setenv(name, elsewhere)
	}
	for _, environment := range [][]string{
		childEnvironment(NewGitRunner(t.TempDir()).Environment, NewGitRunner(t.TempDir()).Unset),
		childEnvironment(NewMachineRunner(testPaths(t)).Environment, NewMachineRunner(testPaths(t)).Unset),
		childEnvironment(NewLiveRunner(t.TempDir()).Environment, NewLiveRunner(t.TempDir()).Unset),
	} {
		for _, entry := range environment {
			name, _, _ := strings.Cut(entry, "=")
			if slices.Contains(gitLocalEnvironment, name) {
				t.Errorf("%s reached a Git child as %q", name, entry)
			}
		}
	}
}

func TestAnExplicitValueBeatsAClearedName(t *testing.T) {
	t.Setenv("GIT_DIR", "/ambient")
	environment := childEnvironment([]string{"GIT_DIR=/chosen"}, gitLocalEnvironment)
	if !slices.Contains(environment, "GIT_DIR=/chosen") {
		t.Fatalf("an explicit value was cleared: %v", environment)
	}
}
