package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdaterUsesCanonicalMiseAndContinuesIndependentSteps(t *testing.T) {
	paths := testPaths(t)
	canonical := misePath(paths)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "commands")
	t.Setenv("UPDATE_TEST_LOG", logPath)
	script := `#!/bin/sh
printf '%s|%s|%s|%s\n' "$*" "$MISE_CONFIG_DIR" "$MISE_GLOBAL_CONFIG_ROOT" "$MISE_CEILING_PATHS" >> "$UPDATE_TEST_LOG"
if [ "$1" = upgrade ]; then
  exit 23
fi
`
	if err := os.WriteFile(canonical, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	updater := NewUpdater(paths, &output)
	updater.Live.Stdout, updater.Live.Stderr = &output, &output
	err := updater.Update()
	if err == nil || !strings.Contains(err.Error(), "Tools") {
		t.Fatalf("Update() error = %v, want Tools failure", err)
	}

	commands, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	selection := filepath.Join(paths.Root, "mise")
	want := strings.Join([]string{
		"self-update --yes|" + selection + "|" + paths.Root + "|" + paths.Root,
		"upgrade --yes|" + selection + "|" + paths.Root + "|" + paths.Root,
		"bootstrap packages upgrade --yes|" + selection + "|" + paths.Root + "|" + paths.Root,
		"bootstrap repos update --yes --skip-dirty|" + selection + "|" + paths.Root + "|" + paths.Root,
		"",
	}, "\n")
	if string(commands) != want {
		t.Fatalf("commands =\n%s\nwant =\n%s", commands, want)
	}
	for _, message := range []string{"standalone mise updated", "declared packages updated", "clean repositories updated"} {
		if !strings.Contains(output.String(), message) {
			t.Fatalf("output missing %q:\n%s", message, output.String())
		}
	}
}

func TestUpdaterRequiresCanonicalMise(t *testing.T) {
	paths := testPaths(t)
	err := NewUpdater(paths, &bytes.Buffer{}).Update()
	if err == nil || !strings.Contains(err.Error(), misePath(paths)) {
		t.Fatalf("Update() error = %v, want the canonical path", err)
	}
}
