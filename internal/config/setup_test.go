package config

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// setupRunner answers each setup probe. A name in unavailable never runs at
// all, which is how a missing tool, a refused execution, and the run deadline
// reach the predicate.
type setupRunner struct {
	answers     map[string]string
	unavailable map[string]bool
}

func (r setupRunner) Run(_ context.Context, name string, args ...string) Result {
	if r.unavailable[name] {
		return Result{Err: &exec.Error{Name: name, Err: exec.ErrNotFound}}
	}
	answer, known := r.answers[name]
	if !known {
		return Result{Err: errors.New("unexpected probe " + name + " " + strings.Join(args, " "))}
	}
	return Result{Stdout: answer}
}

func (setupRunner) Exists(string) bool { return true }

func setupFixture(t *testing.T, runner setupRunner) (Applier, func() []string) {
	t.Helper()
	commands := fakeTools(t,
		fakeTool{name: "hidutil"})
	applier, _ := testApplier(t, testPaths(t), testMachine(), runner)
	return applier, commands
}

func TestSetupReportsAnUnreadableProbeInsteadOfDrift(t *testing.T) {
	paths := testPaths(t)
	runner := setupRunner{
		unavailable: map[string]bool{"hidutil": true},
	}
	checks := setupChecks(paths, runner, macOSFacts(testMachine()))
	var labels []string
	for _, check := range checks {
		if !check.OK {
			labels = append(labels, check.Label)
		}
	}
	if !slices.Contains(labels, "hardware key mapping unreadable") {
		t.Fatalf("an unrunnable probe was not reported as unreadable: %v", labels)
	}
	if slices.Contains(labels, "hardware key mapping present") {
		t.Fatal("an unrunnable probe was reported as drift")
	}
}

func TestConvergeNeverWritesOnAProbeItCouldNotRun(t *testing.T) {
	applier, commands := setupFixture(t, setupRunner{
		unavailable: map[string]bool{"hidutil": true},
	})
	changed, err := applier.converge(macOSFacts(applier.Machine))
	if err == nil {
		t.Fatal("converge hid a probe it could not run")
	}
	if changed != 0 {
		t.Fatalf("converge changed %d facts on an unreadable probe", changed)
	}
	for _, command := range commands() {
		if strings.HasPrefix(command, "hidutil ") {
			t.Fatalf("converge wrote to the Mac on a probe it could not run: %q", command)
		}
	}
}

func TestConvergeClearsHardwareKeyMapping(t *testing.T) {
	applier, commands := setupFixture(t, setupRunner{answers: map[string]string{"hidutil": "(mapped)"}})
	changed, err := applier.converge(macOSFacts(applier.Machine))
	if err != nil || changed != 1 {
		t.Fatalf("converge = %d, %v", changed, err)
	}
	if !slices.Contains(commands(), `hidutil property --set {"UserKeyMapping":[]}`) {
		t.Fatalf("commands = %v", commands())
	}
}

func TestHardwareKeyMappingReadsEveryDevice(t *testing.T) {
	for _, test := range []struct {
		name, output string
		clear        bool
	}{
		{"legacy empty", "(\n)", true},
		{"legacy unset", "(null)", true},
		{"empty devices", "RegistryID Key Value\n100000abc UserKeyMapping (\n)\n100000def UserKeyMapping (null)", true},
		{"mapped later device", "RegistryID Key Value\n100000abc UserKeyMapping ()\n100000def UserKeyMapping ( { HIDKeyboardModifierMappingSrc = 30064771129; HIDKeyboardModifierMappingDst = 30064771113; } )", false},
		{"incomplete device", "RegistryID Key Value\n100000abc UserKeyMapping (", false},
		{"unrecognized output", "property unavailable", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := userKeyMappingClear(test.output); got != test.clear {
				t.Fatalf("clear = %v, want %v", got, test.clear)
			}
		})
	}
}

func TestConvergeLeavesEmptyDeviceMappingsAlone(t *testing.T) {
	applier, commands := setupFixture(t, setupRunner{answers: map[string]string{
		"hidutil": "RegistryID Key Value\n100000abc UserKeyMapping (\n)\n100000def UserKeyMapping (null)",
	}})
	changed, err := applier.converge(macOSFacts(applier.Machine))
	if err != nil || changed != 0 || len(commands()) != 0 {
		t.Fatalf("empty mappings changed: %d, %v, %v", changed, err, commands())
	}
}
