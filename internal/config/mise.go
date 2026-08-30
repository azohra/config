package config

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const minimumMiseVersion = "2026.8.14"

// misePath returns Config's canonical execution substrate.
func misePath(paths Paths) string {
	return paths.InHome(".local", "bin", "mise")
}

func miseVersion(output string) (string, bool) {
	for _, field := range strings.Fields(output) {
		candidate := strings.TrimPrefix(field, "v")
		parts := strings.SplitN(candidate, "-", 2)
		numbers := strings.Split(parts[0], ".")
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
			return parts[0], true
		}
	}
	return "", false
}

func miseVersionAtLeast(output, minimum string) bool {
	current, ok := miseVersion(output)
	if !ok {
		return false
	}
	minimum, ok = miseVersion(minimum)
	if !ok {
		return false
	}
	minimumParts := strings.Split(minimum, ".")
	for index, part := range strings.Split(current, ".") {
		got, _ := strconv.Atoi(part)
		want, _ := strconv.Atoi(minimumParts[index])
		if got != want {
			return got > want
		}
	}
	return true
}

// misePhases are the bootstrap categories Config probes. mise offers no way
// to ask for the aggregate without repos, and repos is the one phase that
// reaches the network: it answers whether a checkout matches its remote,
// which costs a round trip each and is config update's job. Config asks for
// the rest by name and checks repository presence itself.
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

// miseRepositories asks mise which checkouts it declares. Config never reads
// a declaration file: mise parses its own configuration and hands back the
// paths, and what a repository is for stays mise's business.
func miseRepositories(paths Paths, runner Runner) ([]string, error) {
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
	var declared []string
	for _, file := range files {
		// A configuration file that declares no repositories exits non-zero.
		// That is absence, not failure.
		value := run(runner, "mise", "config", "get", "-f", file.Path, "bootstrap.repos")
		if value.Err != nil {
			continue
		}
		var table map[string]any
		if err := toml.Unmarshal([]byte(value.Stdout), &table); err != nil {
			return nil, fmt.Errorf("read declared repositories in %s: %w", file.Path, err)
		}
		for key := range table {
			path := key
			if strings.HasPrefix(path, "~/") {
				path = filepath.Join(paths.Home, strings.TrimPrefix(path, "~/"))
			}
			if path = filepath.Clean(path); !seen[path] {
				seen[path] = true
				declared = append(declared, path)
			}
		}
	}
	slices.Sort(declared)
	return declared, nil
}
