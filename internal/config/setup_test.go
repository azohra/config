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
		fakeTool{name: "defaults"}, fakeTool{name: "hidutil"}, fakeTool{name: "plutil"})
	applier, _ := testApplier(t, testPaths(t), testMachine(), runner)
	return applier, commands
}

const spotlightDeclared = `{"enabled":"0","value":{"type":"standard","parameters":["32","49","1048576"]}}`

func TestSetupProbesReadEveryFieldTheirFixWrites(t *testing.T) {
	// The declared shortcut is bound to different keys. Comparing only
	// enabled reports a match and leaves the Mac on the wrong binding.
	rebound := `{"enabled":"0","value":{"type":"standard","parameters":["49","49","1048576"]}}`
	checks := setupChecks(testPaths(t), setupRunner{answers: map[string]string{
		"defaults": "1", "hidutil": "()", "plutil": rebound,
	}}, miseFacts(testMachine()))
	for _, check := range checks {
		if strings.HasPrefix(check.Label, "Spotlight") && check.OK {
			t.Fatalf("a rebound Spotlight shortcut reported as matching: %#v", check)
		}
	}
}

func TestSetupProbesAcceptEveryScalarSpellingOfTheSameValue(t *testing.T) {
	// macOS and Config have each written this key; a boolean, an integer, and
	// a string can all carry the same value.
	for _, entry := range []string{
		spotlightDeclared,
		`{"enabled":false,"value":{"type":"standard","parameters":[32,49,1048576]}}`,
		`{"enabled":0,"value":{"type":"standard","parameters":["32",49,1048576]}}`,
	} {
		if !spotlightMatches(entry, "0", []string{"32", "49", "1048576"}, "standard") {
			t.Errorf("declared shortcut reported as drift: %s", entry)
		}
	}
	if spotlightMatches(`{"enabled":"1","value":{"type":"standard","parameters":["32","49","1048576"]}}`,
		"0", []string{"32", "49", "1048576"}, "standard") {
		t.Error("a shortcut that is enabled matched a declaration that disables it")
	}
}

func TestSetupReportsAnUnreadableProbeInsteadOfDrift(t *testing.T) {
	paths := testPaths(t)
	runner := setupRunner{
		answers:     map[string]string{"defaults": "1", "plutil": spotlightDeclared},
		unavailable: map[string]bool{"hidutil": true},
	}
	checks := setupChecks(paths, runner, miseFacts(testMachine()))
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
		answers:     map[string]string{"defaults": "1", "plutil": spotlightDeclared},
		unavailable: map[string]bool{"hidutil": true},
	})
	changed, err := applier.converge(miseFacts(applier.Machine))
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

func TestConvergeAttemptsEveryFactAndRunsTheRealFixes(t *testing.T) {
	// Every declared fact drifts, so all three live commands execute — and an
	// unreadable probe in the middle must not hide the facts behind it.
	applier, commands := setupFixture(t, setupRunner{
		answers:     map[string]string{"hidutil": "(mapped)", "plutil": `{"enabled":"1","value":{"type":"standard","parameters":["1","2","3"]}}`},
		unavailable: map[string]bool{"defaults": true},
	})
	changed, err := applier.converge(miseFacts(applier.Machine))
	if err == nil {
		t.Fatal("converge hid the unreadable tap-to-click probe")
	}
	if changed != 2 {
		t.Fatalf("converge changed %d facts, want the two it could read", changed)
	}
	issued := strings.Join(commands(), "\n")
	for _, wanted := range []string{
		"defaults write com.apple.symbolichotkeys AppleSymbolicHotKeys -dict-add 64 ",
		`hidutil property --set {"UserKeyMapping":[]}`,
	} {
		if !strings.Contains(issued, wanted) {
			t.Fatalf("converge never issued %q:\n%s", wanted, issued)
		}
	}
	if strings.Contains(issued, "-currentHost write") {
		t.Fatal("converge wrote tap-to-click from a probe it could not run")
	}
}
