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

func TestMacOSFactsDoNotDependOnMise(t *testing.T) {
	// Hardware key mapping uses hidutil independently of Mise.
	paths := testPaths(t)
	machine := testMachine()
	probes := setupRunner{answers: map[string]string{
		"hidutil": "(mapped)",
	}}

	// Inspection: an unsupported Mise cannot hide native settings beside it.
	inspector := Inspector{Paths: paths, Machine: machine, Runner: probes,
		Mise: unsupportedMiseRunner{setupRunner: probes}}
	resource := inspector.macOS()
	var labels []string
	for _, check := range resource.Checks {
		labels = append(labels, check.Label)
	}
	for _, fact := range macOSFacts(machine) {
		if !slices.Contains(labels, fact.ok) && !slices.Contains(labels, fact.drifted) &&
			!slices.Contains(labels, fact.unreadable) {
			t.Errorf("an unsupported mise hid the %q fact: %v", fact.ok, labels)
		}
	}

	// Apply: Mise can fail while the selected macOS resource still runs.
	commands := fakeTools(t, fakeTool{name: "mise", exit: 1},
		fakeTool{name: "hidutil"})
	applier, chatter := testApplier(t, paths, machine, probes)
	applier.Mise = unsupportedMiseRunner{setupRunner: probes}
	if err := applier.Apply([]Selection{{ID: macOSID, Action: Apply}, {ID: miseID, Action: Apply}}); err == nil {
		t.Fatal("a failed mise bootstrap was reported as success")
	}
	issued := strings.Join(commands(), "\n")
	for _, wanted := range []string{`hidutil property --set`} {
		if !strings.Contains(issued, wanted) {
			t.Errorf("a failed mise skipped %q:\n%s\n%s", wanted, issued, chatter.String())
		}
	}
}

// unsupportedMiseRunner answers the setup probes and reports a mise Config
// does not accept.
type unsupportedMiseRunner struct{ setupRunner }

func (r unsupportedMiseRunner) Run(ctx context.Context, name string, args ...string) Result {
	if name == "mise" {
		if len(args) == 1 && args[0] == "--version" {
			return Result{Stdout: "1999.1.1 macos-arm64"}
		}
		return Result{Err: errors.New("mise is unsupported")}
	}
	return r.setupRunner.Run(ctx, name, args...)
}
