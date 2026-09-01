package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	macOSID   = "macos"
	macOSName = "macOS"
)

// setupFact pairs one desired-state predicate with the fix that converges it.
// Inspector and Applier both read these tables, so drift and apply can never
// disagree about what a fact means. A predicate answers three ways, because a
// probe Config could not perform is not evidence of drift, and writing to the
// Mac on that evidence is the one outcome worse than reporting nothing.
type setupFact struct {
	ok         string
	drifted    string
	unreadable string
	hint       string
	current    func(Paths, Runner) (bool, error)
	fix        func(Applier) error
}

// probed separates a process that ran and disagreed from one that never ran.
// A missing tool, a refused execution, or the run deadline leaves no answer;
// a non-zero exit from a tool that ran is an answer.
func probed(result Result) (string, error) {
	if result.Err != nil && result.ExitCode() < 0 {
		return "", result.Failure()
	}
	return result.Output(), nil
}

func macOSFacts(machine Machine) []setupFact {
	var facts []setupFact
	if desired := machine.MacOS.CurrentHostTapToClick; desired != nil {
		value := "0"
		if *desired {
			value = "1"
		}
		facts = append(facts, setupFact{
			ok: "current-host tap to click matches", drifted: "current-host tap to click differs",
			unreadable: "current-host tap to click unreadable",
			hint:       "apply tracked value",
			current: func(_ Paths, runner Runner) (bool, error) {
				output, err := probed(run(runner, "defaults", "-currentHost", "read", "NSGlobalDomain", "com.apple.mouse.tapBehavior"))
				return output == value, err
			},
			fix: func(e Applier) error {
				return e.Live.Command("defaults", "-currentHost", "write", "NSGlobalDomain", "com.apple.mouse.tapBehavior", "-int", value)
			},
		})
	}
	if spotlight := machine.MacOS.Spotlight; spotlight != nil {
		enabled := "0"
		if spotlight.Enabled {
			enabled = "1"
		}
		parameters := make([]string, len(spotlight.Parameters))
		for index, parameter := range spotlight.Parameters {
			parameters[index] = fmt.Sprint(parameter)
		}
		value := fmt.Sprintf(`{enabled = %s; value = {parameters = (%s); type = "%s"; }; }`,
			enabled, strings.Join(parameters, ", "), spotlight.Type)
		facts = append(facts, setupFact{
			ok: "Spotlight shortcut matches", drifted: "Spotlight shortcut differs",
			unreadable: "Spotlight shortcut unreadable",
			hint:       "apply tracked value",
			current: func(paths Paths, runner Runner) (bool, error) {
				// Read every field the fix writes. Comparing only enabled
				// reports a match for a shortcut bound to different keys.
				key := fmt.Sprintf("AppleSymbolicHotKeys.%d", spotlight.ID)
				entry, err := probed(run(runner, "plutil", "-extract", key, "json", "-o", "-",
					paths.InHome("Library", "Preferences", "com.apple.symbolichotkeys.plist")))
				if err != nil || entry == "" {
					return false, err
				}
				return spotlightMatches(entry, enabled, parameters, spotlight.Type), nil
			},
			fix: func(e Applier) error {
				return e.Live.Command("defaults", "write", "com.apple.symbolichotkeys", "AppleSymbolicHotKeys", "-dict-add", fmt.Sprint(spotlight.ID), value)
			},
		})
	}
	if machine.MacOS.ClearUserKeyMapping {
		facts = append(facts, setupFact{
			ok: "hardware key mapping clear", drifted: "hardware key mapping present",
			unreadable: "hardware key mapping unreadable",
			hint:       "clear it",
			current: func(_ Paths, runner Runner) (bool, error) {
				// A reboot resets hidutil state to (null), which means the same
				// thing as an explicitly empty list: no custom mappings.
				mapping, err := probed(run(runner, "hidutil", "property", "--get", "UserKeyMapping"))
				if err != nil {
					return false, err
				}
				normalized := strings.Join(strings.Fields(mapping), "")
				return normalized == "()" || normalized == "(null)", nil
			},
			fix: func(e Applier) error {
				return e.Live.Command("hidutil", "property", "--set", `{"UserKeyMapping":[]}`)
			},
		})
	}
	return facts
}

// spotlightMatches compares the stored shortcut against the declaration.
// macOS and Config have each written this key, and they spell scalars
// differently — a boolean, an integer, and a string can all carry the same
// value — so every field compares as text.
func spotlightMatches(entry, enabled string, parameters []string, shortcutType string) bool {
	var decoded struct {
		Enabled any `json:"enabled"`
		Value   struct {
			Parameters []any `json:"parameters"`
			Type       any   `json:"type"`
		} `json:"value"`
	}
	if err := json.Unmarshal([]byte(entry), &decoded); err != nil {
		return false
	}
	if scalarText(decoded.Enabled) != enabled || scalarText(decoded.Value.Type) != shortcutType {
		return false
	}
	if len(decoded.Value.Parameters) != len(parameters) {
		return false
	}
	for index, parameter := range decoded.Value.Parameters {
		if scalarText(parameter) != parameters[index] {
			return false
		}
	}
	return true
}

func scalarText(value any) string {
	switch typed := value.(type) {
	case bool:
		if typed {
			return "1"
		}
		return "0"
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case string:
		return typed
	default:
		return ""
	}
}

func setupChecks(paths Paths, runner Runner, facts []setupFact) []Check {
	var checks []Check
	for _, fact := range facts {
		current, err := fact.current(paths, runner)
		switch {
		case err != nil:
			checks = append(checks, no(fact.unreadable, err.Error()))
		case current:
			checks = append(checks, yes(fact.ok))
		default:
			checks = append(checks, no(fact.drifted, fact.hint))
		}
	}
	return checks
}

// converge fixes each drifted fact and reports how many facts changed. Every
// fact is attempted: one unreadable probe or one failed fix must not hide the
// facts behind it, and on a pending bootstrap must not abort the restore.
func (e Applier) converge(facts []setupFact) (int, error) {
	changed := 0
	var failures []error
	for _, fact := range facts {
		current, err := fact.current(e.Paths, e.Runner)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", fact.unreadable, err))
			continue
		}
		if current {
			continue
		}
		if err := fact.fix(e); err != nil {
			failures = append(failures, err)
			continue
		}
		changed++
	}
	return changed, errors.Join(failures...)
}
