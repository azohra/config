package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validMachineTOML() string {
	return `
kind = "azohra.config.machine"
schema = 2
dock = true
chrome_pwas = true
finder_favorites = true

[[repository_hooks]]
name = "post-checkout"
source = "hooks/post-checkout"

[repository]
branch = "main"
url = "https://example.com/owner/machine.git"

[macos]
current_host_tap_to_click = true
clear_user_key_mapping = true

[macos.spotlight]
id = 64
enabled = false
parameters = [32, 49, 1048576]
type = "standard"

[[preferences]]
id = "example-app"
name = "Example App"
bundle = "com.example.ExampleApp"
domain = "com.example.ExampleApp"

`
}

func writeMachineTOML(t *testing.T, content string) Paths {
	t.Helper()
	paths := testPaths(t)
	if err := os.WriteFile(paths.InRoot("config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return paths
}

func TestLoadMachineReadsStrictContract(t *testing.T) {
	paths := writeMachineTOML(t, validMachineTOML())
	machine, err := LoadMachine(paths)
	if err != nil {
		t.Fatal(err)
	}
	if machine.Repository.Destination() != "origin/main" || !machine.Dock || !machine.ChromePWAs || !machine.FinderFavorites {
		t.Fatalf("unexpected machine contract: %+v", machine)
	}
	if len(machine.Preferences) != 1 {
		t.Fatalf("typed rows were not decoded: %+v", machine)
	}
	if len(machine.RepositoryHooks) != 1 || machine.RepositoryHooks[0].Name != "post-checkout" ||
		machine.RepositoryHooks[0].Source != "hooks/post-checkout" {
		t.Fatalf("repository hooks were not decoded: %+v", machine.RepositoryHooks)
	}
}

func TestLoadMachineRejectsWrongIdentityAndUnknownFields(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
		want    string
	}{
		{"empty contract", "", "kind is"},
		{"wrong kind", strings.Replace(validMachineTOML(), MachineKind, "another.machine", 1), "kind is"},
		{"wrong schema", strings.Replace(validMachineTOML(), "schema = 2", "schema = 1", 1), "schema is 1"},
		{"unknown field", strings.Replace(validMachineTOML(), "schema = 2", "schema = 2\ntyop = true", 1), "strict mode"},
		{"removed singular favorite", strings.Replace(validMachineTOML(), "finder_favorites = true", "[finder_favorite]\nname = \"Machine config\"", 1), "strict mode"},
		{"invalid hook name", strings.Replace(validMachineTOML(), `name = "post-checkout"`, `name = "../post-checkout"`, 1), "repository_hooks name"},
		{"absolute hook source", strings.Replace(validMachineTOML(), `source = "hooks/post-checkout"`, `source = "/tmp/post-checkout"`, 1), "relative file path"},
		{"escaping hook source", strings.Replace(validMachineTOML(), `source = "hooks/post-checkout"`, `source = "../post-checkout"`, 1), "relative file path"},
		{"duplicate hook", strings.Replace(validMachineTOML(), "[repository]", "[[repository_hooks]]\nname = \"post-checkout\"\nsource = \"hooks/another\"\n\n[repository]", 1), "repeats name"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := LoadMachine(writeMachineTOML(t, test.content)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want it to contain %q", err, test.want)
			}
		})
	}
}

func TestLoadMachineAcceptsOnlyRepositoryIdentity(t *testing.T) {
	content := `
kind = "azohra.config.machine"
schema = 2

[repository]
branch = "main"
url = "https://example.com/owner/machine.git"
`
	machine, err := LoadMachine(writeMachineTOML(t, content))
	if err != nil {
		t.Fatal(err)
	}
	if machine.Dock || machine.ChromePWAs || machine.FinderFavorites || len(machine.RepositoryHooks) != 0 || len(machine.Preferences) != 0 {
		t.Fatalf("undeclared capabilities were enabled: %+v", machine)
	}
}

func TestUndeclaredCapabilitiesDoNotBecomeResources(t *testing.T) {
	machine := testMachine()
	machine.Dock = false
	machine.ChromePWAs = false
	machine.Preferences = nil
	machine.RepositoryHooks = nil
	report := NewInspector(testPaths(t), machine, converged{}).Inspect()
	if len(report.Resources) != 1 || report.Resources[0].ID != setupID {
		t.Fatalf("undeclared capabilities became resources: %+v", report.Resources)
	}
}

func TestConfigOwnsSnapshotPaths(t *testing.T) {
	paths := testPaths(t)
	preference := testMachine().Preferences[0]
	tests := map[string]string{
		"Dock":             dockSnapshotPath(paths),
		"Chrome PWAs":      chromePWASnapshotPath(paths),
		"Chrome PWA icons": chromePWAIconDir(paths),
		"Finder Favorites": finderFavoritesSnapshotPath(paths),
		"preference":       preference.snapshotPath(paths),
	}
	want := map[string]string{
		"Dock":             paths.InRoot("snapshots", "dock.apps"),
		"Chrome PWAs":      paths.InRoot("snapshots", "chrome-pwas.json"),
		"Chrome PWA icons": paths.InRoot("snapshots", "chrome-pwas"),
		"Finder Favorites": paths.InRoot("snapshots", "finder-favorites.json"),
		"preference":       paths.InRoot("snapshots", "preferences", preference.ID+".plist"),
	}
	for name, got := range tests {
		if got != want[name] {
			t.Errorf("%s snapshot path = %q, want %q", name, got, want[name])
		}
	}
}

func TestLoadMachineRefusesAManagedRootSymlink(t *testing.T) {
	real := writeMachineTOML(t, validMachineTOML())
	home := t.TempDir()
	paths := Paths{
		Root:     filepath.Join(home, "managed", "repository"),
		Home:     home,
		StateDir: filepath.Join(home, "state"),
	}
	if err := os.MkdirAll(filepath.Dir(paths.Root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real.Root, paths.Root); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMachine(paths); err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("managed symlink error = %v", err)
	}
}

func TestMiseEnvironmentNamesTheSelectedRootWithoutMutatingTheProcess(t *testing.T) {
	paths := testPaths(t)
	t.Setenv("MISE_CONFIG_DIR", "before")
	t.Setenv("MISE_GLOBAL_CONFIG_ROOT", "before")
	t.Setenv("MISE_CEILING_PATHS", "before")
	t.Setenv("MISE_AUTO_UPDATE", "before")
	environment := miseEnvironment(paths)
	if got := environment[0]; got != "MISE_AUTO_UPDATE=0" {
		t.Fatalf("machine environment = %v", environment)
	}
	if got := environment[1]; got != "MISE_CONFIG_DIR="+filepath.Join(paths.Root, "mise") {
		t.Fatalf("machine environment = %v", environment)
	}
	if got := environment[2]; got != "MISE_GLOBAL_CONFIG_ROOT="+paths.Root {
		t.Fatalf("machine environment = %v", environment)
	}
	if got := environment[3]; got != "MISE_CEILING_PATHS="+paths.Root {
		t.Fatalf("machine environment = %v", environment)
	}
	if os.Getenv("MISE_CONFIG_DIR") != "before" || os.Getenv("MISE_GLOBAL_CONFIG_ROOT") != "before" || os.Getenv("MISE_CEILING_PATHS") != "before" || os.Getenv("MISE_AUTO_UPDATE") != "before" {
		t.Fatal("building a child environment mutated the Config process")
	}
}

func TestMachineRefusesAPreferenceIdConfigAlreadyAnswersTo(t *testing.T) {
	// Reports, selections, and baselines are all keyed by resource id, so a
	// preference borrowing a capability's id collides with that capability.
	for _, id := range []string{"setup", "dock", "chrome-pwas", "finder-favorites", "repository-hooks"} {
		machine := testMachine()
		machine.Preferences[0].ID = id
		if err := machine.Validate(); err == nil {
			t.Errorf("preference id %q was accepted", id)
		}
	}
	machine := testMachine()
	machine.Preferences[0].ID = "example-app"
	if err := machine.Validate(); err != nil {
		t.Fatalf("an ordinary preference id was refused: %v", err)
	}
}
