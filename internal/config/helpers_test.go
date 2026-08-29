package config

import (
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
