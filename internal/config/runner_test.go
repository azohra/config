package config

import (
	"bytes"
	"context"
	"os"
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
