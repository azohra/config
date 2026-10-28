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
schema = 7
chrome_pwas = true
finder_favorites = true

[repository]
branch = "main"
url = "https://example.com/owner/machine.git"

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
	if machine.Repository.Destination() != "origin/main" || !machine.ChromePWAs || !machine.FinderFavorites {
		t.Fatalf("unexpected machine contract: %+v", machine)
	}
	if len(machine.Preferences) != 1 {
		t.Fatalf("typed rows were not decoded: %+v", machine)
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
		{"wrong schema", strings.Replace(validMachineTOML(), "schema = 7", "schema = 1", 1), "schema is 1"},
		{"implicit Mise schema", strings.Replace(validMachineTOML(), "schema = 7", "schema = 2", 1), "schema is 2"},
		{"removed Mise resource", "mise = true\n" + validMachineTOML(), "strict mode"},
		{"removed skills", validMachineTOML() + "\n[agent_skills]\nagents = [\"codex\"]\n", "strict mode"},
		{"removed MCP", validMachineTOML() + "\n[mcp_servers]\nagents = [\"codex\"]\n", "strict mode"},
		{"removed hooks", validMachineTOML() + "\n[[repository_hooks]]\nname = \"post-checkout\"\nsource = \"hook\"\n", "strict mode"},
		{"removed Dock toggle", "dock = true\n" + validMachineTOML(), "strict mode"},
		{"previous schema", strings.Replace(validMachineTOML(), "schema = 7", "schema = 6", 1), "schema is 6"},
		{"removed key mappings", validMachineTOML() + "\n[macos]\nclear_user_key_mapping = true\n", "strict mode"},
		{"unknown field", strings.Replace(validMachineTOML(), "schema = 7", "schema = 7\ntyop = true", 1), "strict mode"},
		{"removed singular favorite", strings.Replace(validMachineTOML(), "finder_favorites = true", "[finder_favorite]\nname = \"Machine config\"", 1), "strict mode"},
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
schema = 7

[repository]
branch = "main"
url = "https://example.com/owner/machine.git"
`
	machine, err := LoadMachine(writeMachineTOML(t, content))
	if err != nil {
		t.Fatal(err)
	}
	if machine.ChromePWAs || machine.FinderFavorites || len(machine.Preferences) != 0 {
		t.Fatalf("undeclared capabilities were enabled: %+v", machine)
	}
}

func TestUndeclaredCapabilitiesDoNotBecomeResources(t *testing.T) {
	machine := testMachine()

	machine.ChromePWAs = false
	machine.Preferences = nil

	report := NewInspector(testPaths(t), machine, converged{}).Inspect()
	if len(report.Resources) != 0 {
		t.Fatalf("undeclared capabilities became resources: %+v", report.Resources)
	}
}

func TestConfigOwnsSnapshotPaths(t *testing.T) {
	paths := testPaths(t)
	preference := testMachine().Preferences[0]
	tests := map[string]string{
		"Chrome PWAs":      chromePWASnapshotPath(paths),
		"Chrome PWA icons": chromePWAIconDir(paths),
		"Finder Favorites": finderFavoritesSnapshotPath(paths),
		"preference":       preference.snapshotPath(paths),
	}
	want := map[string]string{
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

func TestMachineRefusesAPreferenceIdConfigAlreadyAnswersTo(t *testing.T) {
	// Reports, selections, and baselines are all keyed by resource id, so a
	// preference borrowing a capability's id collides with that capability.
	for _, id := range []string{"chrome-pwas", "finder-favorites"} {
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
