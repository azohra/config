package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const (
	MachineKind   = "azohra.config.machine"
	MachineSchema = 3
	managedRemote = "origin"
)

// Machine is Config's complete contract inside config.toml.
type Machine struct {
	Kind            string             `toml:"kind"`
	Schema          int                `toml:"schema"`
	Repository      MachineRepository  `toml:"repository"`
	Mise            bool               `toml:"mise"`
	Dock            bool               `toml:"dock"`
	ChromePWAs      bool               `toml:"chrome_pwas"`
	FinderFavorites bool               `toml:"finder_favorites"`
	RepositoryHooks []RepositoryHook   `toml:"repository_hooks"`
	MacOS           MachineMacOS       `toml:"macos"`
	Preferences     []PreferenceBackup `toml:"preferences"`
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

// reservedResourceIDs are the identifiers Config's own capabilities answer to.
// A preference that borrowed one would collide with that capability wherever a
// report, a selection, or a baseline is keyed by resource id.
var reservedResourceIDs = []string{
	miseID, macOSID, dockID, chromePWAsID, finderFavoritesID, repositoryHooksID,
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
	seenHooks := map[string]bool{}
	for _, hook := range m.RepositoryHooks {
		if !contractIDPattern.MatchString(hook.Name) {
			return fmt.Errorf("repository_hooks name %q is invalid", hook.Name)
		}
		if seenHooks[hook.Name] {
			return fmt.Errorf("repository_hooks repeats name %q", hook.Name)
		}
		seenHooks[hook.Name] = true
		if hook.Source == "" || !filepath.IsLocal(hook.Source) || filepath.Clean(hook.Source) == "." {
			return fmt.Errorf("repository_hooks %q source must be a relative file path", hook.Name)
		}
	}
	seenPreferences := map[string]bool{}
	for _, preference := range m.Preferences {
		if !contractIDPattern.MatchString(preference.ID) || preference.Name == "" {
			return fmt.Errorf("preference %q has an invalid id or empty name", preference.ID)
		}
		if slices.Contains(reservedResourceIDs, preference.ID) {
			return fmt.Errorf("preference id %q is one of Config's own capabilities", preference.ID)
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

// miseEnvironment selects the repository's native Mise configuration for a
// command issued by the Mise resource and stops discovery at the managed
// checkout.
var miseLocalEnvironment = []string{
	"MISE_AUTO_UPDATE",
	"MISE_NO_CONFIG",
	"MISE_CONFIG_DIR",
	"MISE_GLOBAL_CONFIG_ROOT",
	"MISE_CEILING_PATHS",
	"MISE_GLOBAL_CONFIG_FILE",
	"MISE_SYSTEM_CONFIG_FILE",
	"MISE_IGNORED_CONFIG_PATHS",
}

func miseEnvironment(paths Paths) []string {
	return []string{
		"MISE_AUTO_UPDATE=0",
		"MISE_CONFIG_DIR=" + paths.InRoot("mise"),
		"MISE_GLOBAL_CONFIG_ROOT=" + paths.Root,
		"MISE_CEILING_PATHS=" + paths.Root,
		// Naming the configuration directory is not enough. An ambient
		// MISE_GLOBAL_CONFIG_FILE replaces the machine document, a system
		// config file is loaded alongside it, and an ignored path erases it —
		// each one changing what Config converges the Mac against. Empty
		// values leave the selection to the four names above.
		"MISE_GLOBAL_CONFIG_FILE=",
		"MISE_SYSTEM_CONFIG_FILE=",
		"MISE_IGNORED_CONFIG_PATHS=",
	}
}
