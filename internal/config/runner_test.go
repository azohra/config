package config

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandEnvironmentReplacesAmbientSelectionForChildOnly(t *testing.T) {
	t.Setenv("MISE_GLOBAL_CONFIG_FILE", "ambient")

	environment := ChildEnvironment([]string{"MISE_GLOBAL_CONFIG_FILE=child"})
	var selections []string
	for _, entry := range environment {
		if strings.HasPrefix(entry, "MISE_GLOBAL_CONFIG_FILE=") {
			selections = append(selections, entry)
		}
	}
	if len(selections) != 1 || selections[0] != "MISE_GLOBAL_CONFIG_FILE=child" {
		t.Fatalf("child selections = %v", selections)
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
