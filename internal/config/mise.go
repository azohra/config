package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const testedMiseVersion = "2026.8.14"

// misePath returns Config's canonical execution substrate.
func misePath(paths Paths) string {
	return paths.InHome(".local", "bin", "mise")
}

func miseVersion(output string) (string, bool) {
	for _, field := range strings.Fields(output) {
		candidate := strings.TrimPrefix(field, "v")
		numbers := strings.Split(candidate, ".")
		if len(numbers) != 3 {
			continue
		}
		valid := true
		for _, number := range numbers {
			if _, err := strconv.Atoi(number); err != nil {
				valid = false
				break
			}
		}
		if valid {
			return candidate, true
		}
	}
	return "", false
}

func currentMiseVersion(runner Runner) (string, error) {
	if !runner.Exists("mise") {
		return "", errors.New("mise is unavailable")
	}
	result := run(runner, "mise", "--version")
	version, parsed := miseVersion(result.Stdout)
	if result.Err != nil || !parsed {
		return "", errors.New("mise version is unreadable")
	}
	return version, nil
}

// requireTestedMise is the mutation gate for Config-owned mise commands.
// Inspection may report an unsupported version, while only config update is
// allowed to replace it.
func requireTestedMise(runner Runner) error {
	version, err := currentMiseVersion(runner)
	if err != nil {
		return err
	}
	if !supportsTestedMise(version) {
		return fmt.Errorf("mise %s is unsupported; install mise %s", version, testedMiseVersion)
	}
	return nil
}

func supportsTestedMise(version string) bool {
	return version == testedMiseVersion
}

// misePhases are the bootstrap categories Config probes. mise offers no way
// to ask for the aggregate without repos, and repos is the one phase that
// reaches the network: it answers whether a checkout matches its remote,
// which costs a round trip each and belongs to the repository update scope.
// Config asks for the rest by name and checks repository presence itself.
//
// TestMisePhasesCoverEveryBootstrapPhase pins this list to what mise offers,
// so a phase added upstream fails the build instead of going unreported.
var misePhases = [][]string{
	{"accounts"},
	{"compose"},
	{"dotfiles"},
	{"files"},
	{"firewall"},
	{"mise-shell-activate"},
	{"packages"},
	{"plugins"},
	{"secrets"},
	{"services"},
	{"user"},
	{"macos", "defaults"},
	{"macos", "launchd-agents"},
}

// miseRepository is one checkout mise declares: where it belongs, and which
// repository belongs there.
type miseRepository struct {
	Path string
	URL  string
}

// miseRepositories asks mise which checkouts it declares. Config never reads
// a declaration file: mise parses its own configuration and hands back the
// paths, and what a repository is for stays mise's business.
func miseRepositories(paths Paths, runner Runner) ([]miseRepository, error) {
	listing := run(runner, "mise", "config", "ls", "-J")
	if listing.Err != nil {
		return nil, fmt.Errorf("list mise configuration: %w", listing.Failure())
	}
	var files []struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(listing.Stdout), &files); err != nil {
		return nil, fmt.Errorf("read mise configuration list: %w", err)
	}
	seen := make(map[string]bool)
	var declared []miseRepository
	for _, file := range files {
		// A configuration file that declares no repositories exits non-zero.
		// That is absence, not failure.
		value := run(runner, "mise", "config", "get", "-f", file.Path, "bootstrap.repos")
		if value.Err != nil {
			continue
		}
		var table map[string]struct {
			URL string `toml:"url"`
		}
		if err := toml.Unmarshal([]byte(value.Stdout), &table); err != nil {
			return nil, fmt.Errorf("read declared repositories in %s: %w", file.Path, err)
		}
		for key, declaration := range table {
			path := key
			if strings.HasPrefix(path, "~/") {
				path = filepath.Join(paths.Home, strings.TrimPrefix(path, "~/"))
			}
			if path = filepath.Clean(path); !seen[path] {
				seen[path] = true
				declared = append(declared, miseRepository{Path: path, URL: declaration.URL})
			}
		}
	}
	slices.SortFunc(declared, func(left, right miseRepository) int {
		return strings.Compare(left.Path, right.Path)
	})
	return declared, nil
}
