package config

import (
	"errors"
	"fmt"
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
