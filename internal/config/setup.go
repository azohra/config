package config

import (
	"fmt"
	"strings"
)

const (
	setupID   = "setup"
	setupName = "Machine setup"
)

// setupFact pairs one desired-state predicate with the fix that converges it.
// Inspector and Applier both read these tables, so drift and apply can never
// disagree about what a fact means.
type setupFact struct {
	ok      string
	drifted string
	hint    string
	current func(Paths, Runner) bool
	fix     func(Applier) error
}

func miseFacts(machine Machine) []setupFact {
	var facts []setupFact
	if desired := machine.MacOS.CurrentHostTapToClick; desired != nil {
		value := "0"
		if *desired {
			value = "1"
		}
		facts = append(facts, setupFact{
			ok: "current-host tap to click matches", drifted: "current-host tap to click differs",
			hint: "apply tracked value",
			current: func(_ Paths, runner Runner) bool {
				return run(runner, "defaults", "-currentHost", "read", "NSGlobalDomain", "com.apple.mouse.tapBehavior").Output() == value
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
			hint: "apply tracked value",
			current: func(paths Paths, runner Runner) bool {
				key := fmt.Sprintf("AppleSymbolicHotKeys.%d.enabled", spotlight.ID)
				return run(runner, "plutil", "-extract", key, "raw", paths.InHome("Library", "Preferences", "com.apple.symbolichotkeys.plist")).Output() == enabled
			},
			fix: func(e Applier) error {
				return e.Live.Command("defaults", "write", "com.apple.symbolichotkeys", "AppleSymbolicHotKeys", "-dict-add", fmt.Sprint(spotlight.ID), value)
			},
		})
	}
	if machine.MacOS.ClearUserKeyMapping {
		facts = append(facts, setupFact{
			ok: "hardware key mapping clear", drifted: "hardware key mapping present",
			hint: "clear it",
			current: func(_ Paths, runner Runner) bool {
				// A reboot resets hidutil state to (null), which means the same
				// thing as an explicitly empty list: no custom mappings.
				mapping := run(runner, "hidutil", "property", "--get", "UserKeyMapping")
				normalized := strings.Join(strings.Fields(mapping.Stdout), "")
				return normalized == "()" || normalized == "(null)"
			},
			fix: func(e Applier) error {
				return e.Live.Command("hidutil", "property", "--set", `{"UserKeyMapping":[]}`)
			},
		})
	}
	return facts
}

func setupChecks(paths Paths, runner Runner, facts []setupFact) []Check {
	var checks []Check
	for _, fact := range facts {
		if fact.current(paths, runner) {
			checks = append(checks, yes(fact.ok))
		} else {
			checks = append(checks, no(fact.drifted, fact.hint))
		}
	}
	return checks
}

// converge fixes each drifted fact and reports how many facts changed.
func (e Applier) converge(facts []setupFact) (int, error) {
	changed := 0
	for _, fact := range facts {
		if fact.current(e.Paths, e.Runner) {
			continue
		}
		if err := fact.fix(e); err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}
