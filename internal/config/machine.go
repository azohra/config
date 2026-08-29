package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const (
	MachineKind   = "azohra.config.machine"
	MachineSchema = 1
	managedRemote = "origin"
)

// Machine is Config's complete contract inside config.toml.
type Machine struct {
	Kind        string             `toml:"kind"`
	Schema      int                `toml:"schema"`
	Repository  MachineRepository  `toml:"repository"`
	Dock        bool               `toml:"dock"`
	ChromePWAs  bool               `toml:"chrome_pwas"`
	MacOS       MachineMacOS       `toml:"macos"`
	Preferences []PreferenceBackup `toml:"preferences"`
}

type MachineRepository struct {
	Branch string `toml:"branch"`
	URL    string `toml:"url"`
}

func (r MachineRepository) Destination() string {
	return managedRemote + "/" + r.Branch
}

type MachineMacOS struct {
	CurrentHostTapToClick *bool              `toml:"current_host_tap_to_click"`
	ClearUserKeyMapping   bool               `toml:"clear_user_key_mapping"`
	Spotlight             *SpotlightShortcut `toml:"spotlight"`
}

type SpotlightShortcut struct {
	ID         int    `toml:"id"`
	Enabled    bool   `toml:"enabled"`
	Parameters []int  `toml:"parameters"`
	Type       string `toml:"type"`
}

var (
	contractIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	gitNamePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	bundleIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`)
)

func LoadMachine(paths Paths) (Machine, error) {
	root, err := os.Lstat(paths.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return Machine{}, fmt.Errorf("no managed configuration at %s; run config bootstrap <repository>", paths.Root)
		}
		return Machine{}, fmt.Errorf("inspect managed configuration: %w", err)
	}
	if root.Mode()&os.ModeSymlink != 0 || !root.IsDir() {
		return Machine{}, fmt.Errorf("managed checkout %s is not a directory", paths.Root)
	}
	filename := paths.InRoot("config.toml")
	data, err := os.ReadFile(filename)
	if err != nil {
		return Machine{}, fmt.Errorf("read machine contract: %w", err)
	}

	var machine Machine
	decoder := toml.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&machine); err != nil {
		return Machine{}, fmt.Errorf("decode %s: %w", filename, err)
	}
	if err := machine.Validate(); err != nil {
		return Machine{}, fmt.Errorf("%s: %w", filename, err)
	}
	return machine, nil
}

func (m Machine) Validate() error {
	if m.Kind != MachineKind {
		return fmt.Errorf("kind is %q, want %q", m.Kind, MachineKind)
	}
	if m.Schema != MachineSchema {
		return fmt.Errorf("schema is %d, want %d", m.Schema, MachineSchema)
	}
	if _, err := repositoryIdentity(m.Repository.URL); err != nil {
		return fmt.Errorf("repository.url: %w", err)
	}
	if !validGitName(m.Repository.Branch) {
		return fmt.Errorf("repository.branch %q is invalid", m.Repository.Branch)
	}
	if m.MacOS.Spotlight != nil {
		spotlight := m.MacOS.Spotlight
		if spotlight.ID <= 0 || spotlight.Type != "standard" || len(spotlight.Parameters) != 3 {
			return fmt.Errorf("macos.spotlight must declare a positive id, type standard, and three parameters")
		}
	}
	seenPreferences := map[string]bool{}
	for _, preference := range m.Preferences {
		if !contractIDPattern.MatchString(preference.ID) || preference.Name == "" {
			return fmt.Errorf("preference %q has an invalid id or empty name", preference.ID)
		}
		if seenPreferences[preference.ID] {
			return fmt.Errorf("preferences repeats id %q", preference.ID)
		}
		seenPreferences[preference.ID] = true
		if !bundleIDPattern.MatchString(preference.Bundle) || !bundleIDPattern.MatchString(preference.Domain) {
			return fmt.Errorf("preference %q has an invalid bundle or domain", preference.ID)
		}
	}
	return nil
}

func validGitName(value string) bool {
	return gitNamePattern.MatchString(value) && !strings.Contains(value, "..") &&
		!strings.HasSuffix(value, ".") && !strings.HasSuffix(value, "/")
}

// MiseEnvironment selects the repository's native mise configuration for a
// Config child process and stops discovery at the managed checkout.
func MiseEnvironment(paths Paths) []string {
	return []string{
		"MISE_CONFIG_DIR=" + paths.InRoot("mise"),
		"MISE_GLOBAL_CONFIG_ROOT=" + paths.Root,
		"MISE_CEILING_PATHS=" + paths.Root,
	}
}
