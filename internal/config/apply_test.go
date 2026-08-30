package config

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Every step the Logger writes must read back as one: a reader that colors or
// filters this output depends on the writer and StepGlyph agreeing.
func TestStepGlyphRecognizesEveryLoggerStep(t *testing.T) {
	var out bytes.Buffer
	log := Logger{Out: &out}
	log.OK("pushed")
	log.Info("validating")
	log.Warn("commit remains local")
	log.Error("push rejected")

	for _, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		glyph, ok := StepGlyph(line)
		if !ok {
			t.Fatalf("StepGlyph did not recognize the Logger's own %q", line)
		}
		if !strings.HasPrefix(strings.TrimLeft(line, " "), glyph+" ") {
			t.Fatalf("StepGlyph(%q) = %q, which the line does not lead with", line, glyph)
		}
	}

	var section bytes.Buffer
	Logger{Out: &section}.Section("Snapshot")
	notSteps := append(strings.Split(section.String(), "\n"),
		"[check] ~/.gitconfig  symlink  applied", " 1 file changed", "  1 file changed", "  ✓", "✓ unindented", "  ↔ no Logger writes this")
	for _, line := range notSteps {
		if glyph, ok := StepGlyph(line); ok {
			t.Fatalf("StepGlyph(%q) claimed glyph %q", line, glyph)
		}
	}
}

// converged answers every miseFacts probe as already-correct, so applyMise
// reaches its one live command and stops: nothing to fix, no restarts.
type converged struct{}

func (converged) Run(_ context.Context, name string, args ...string) Result {
	switch {
	case name == "defaults" && slices.Contains(args, "com.apple.mouse.tapBehavior"):
		return Result{Stdout: "1\n"}
	case name == "plutil":
		return Result{Stdout: "0\n"}
	case name == "hidutil":
		return Result{Stdout: "()\n"}
	}
	return Result{}
}

func (converged) Exists(string) bool { return true }

// One dirty checkout must not block the rest of machine reconciliation.
func TestADirtyCheckoutDoesNotBlockApply(t *testing.T) {
	fakeBin := t.TempDir()
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	commandLog := filepath.Join(t.TempDir(), "commands")
	t.Setenv("COMMAND_LOG", commandLog)
	mise := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$COMMAND_LOG\"\nfor arg in \"$@\"; do [ \"$arg\" = --skip-dirty ] && exit 0; done\n" +
		"echo 'repos: ~/Projects/example has local changes' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "mise"), []byte(mise), 0o755); err != nil {
		t.Fatal(err)
	}

	var chatter bytes.Buffer
	applier := Applier{
		Paths:   testPaths(t),
		Machine: testMachine(),
		Runner:  converged{},
		Live:    LiveRunner{Stdout: &chatter, Stderr: &chatter},
		Log:     Logger{Out: &chatter},
	}
	if err := applier.applyMise(); err != nil {
		t.Fatalf("a dirty checkout blocked apply: %v", err)
	}
	commands, err := os.ReadFile(commandLog)
	if err != nil {
		t.Fatal(err)
	}
	want := "bootstrap --yes --skip-dirty\n"
	if string(commands) != want {
		t.Fatalf("mise order = %q, want %q", commands, want)
	}
}

// applyRunner answers the probes an Applier makes while reconciling, so a
// test can drive apply without a real Mac.
type applyRunner struct {
	dock string
}

func (r applyRunner) Run(_ context.Context, name string, args ...string) Result {
	switch {
	case name == "dockutil" && slices.Equal(args, []string{"--list"}):
		return Result{Stdout: r.dock}
	case name == "defaults" && len(args) > 0 && args[0] == "export":
		return Result{Stdout: "<?xml version=\"1.0\"?><plist version=\"1.0\"><dict><key>k</key><true/></dict></plist>"}
	case name == "mdfind":
		return Result{Stdout: "/Applications/Example.app"}
	case name == "osascript":
		return Result{Stdout: "false"}
	}
	return Result{}
}

func (applyRunner) Exists(string) bool { return true }

// Apply is a plan executor: a step runs because it was chosen, not because
// the machine declares it.
func TestApplyRunsOnlyTheSelectedSteps(t *testing.T) {
	paths := testPaths(t)
	app := paths.InHome("Applications", "Example.app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := applyRunner{dock: "Example\tfile://" + app + "/\tpersistentApps\n"}
	applier, chatter := testApplier(t, paths, testMachine(), runner)

	if err := applier.Apply([]Selection{{ID: dockID, Action: Capture}}); err != nil {
		t.Fatal(err)
	}
	out := chatter.String()
	if !strings.Contains(out, dockName) {
		t.Fatalf("the selected step did not run:\n%s", out)
	}
	for _, unselected := range []string{setupName, chromePWAsName, "Example App"} {
		if strings.Contains(out, unselected) {
			t.Fatalf("%s ran without being selected:\n%s", unselected, out)
		}
	}
	if _, err := os.Stat(dockSnapshotPath(paths)); err != nil {
		t.Fatalf("the selected capture wrote nothing: %v", err)
	}
}

// A step that skips itself deliberately is not a failure. Apply must not
// convert that warning into an error through the baseline pass.
func TestApplyDoesNotFailOnADeliberateSkip(t *testing.T) {
	paths := testPaths(t)
	machine := testMachine()
	machine.ChromePWAs = false
	machine.Preferences = nil
	runner := applyRunner{dock: ""}
	applier, chatter := testApplier(t, paths, machine, runner)

	// No saved layout exists, so applyDock declines and says so.
	err := applier.Apply([]Selection{{ID: dockID, Action: Apply}})
	if err != nil {
		t.Fatalf("a deliberate skip was reported as a failure: %v", err)
	}
	if !strings.Contains(chatter.String(), "Dock left untouched") {
		t.Fatalf("the skip was not explained:\n%s", chatter.String())
	}
}

// The Dock is rebuilt with one dockutil call per planned change, each
// suppressing the restart, and a single restart at the end. Dropping
// --no-restart makes the Dock flicker through every intermediate layout.
func TestApplyDockIssuesThePlannedOperationsThenRestartsOnce(t *testing.T) {
	paths := testPaths(t)
	machine := testMachine()
	machine.ChromePWAs = false
	machine.Preferences = nil
	first := paths.InHome("Applications", "First.app")
	second := paths.InHome("Applications", "Second.app")
	for _, app := range []string{first, second} {
		if err := os.MkdirAll(app, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := atomicWrite(dockSnapshotPath(paths), []byte(first+"\n"+second+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The Dock reads reversed, then reads back in the saved order once the
	// planned dockutil calls have run.
	reversed := "Second\tfile://" + second + "/\tpersistentApps\nFirst\tfile://" + first + "/\tpersistentApps\n"
	restored := "First\tfile://" + first + "/\tpersistentApps\nSecond\tfile://" + second + "/\tpersistentApps\n"
	runner := &sequencedDockRunner{listings: []string{reversed, restored}}
	commands := fakeTools(t, fakeTool{name: "dockutil"}, fakeTool{name: "killall"})
	applier, chatter := testApplier(t, paths, machine, runner)

	if err := applier.Apply([]Selection{{ID: dockID, Action: Apply}}); err != nil {
		t.Fatalf("apply Dock: %v\n%s", err, chatter.String())
	}
	issued := commands()
	if len(issued) == 0 {
		t.Fatalf("apply issued no commands:\n%s", chatter.String())
	}
	restarts := 0
	for _, command := range issued {
		switch {
		case strings.HasPrefix(command, "dockutil "):
			if !strings.Contains(command, "--no-restart") {
				t.Fatalf("a Dock change restarted the Dock mid-plan: %q", command)
			}
		case strings.HasPrefix(command, "killall "):
			restarts++
			if !strings.Contains(command, "Dock") {
				t.Fatalf("unexpected killall: %q", command)
			}
		default:
			t.Fatalf("apply issued an unexpected command: %q", command)
		}
	}
	if restarts != 1 {
		t.Fatalf("Dock restarted %d times, want exactly 1:\n%s", restarts, strings.Join(issued, "\n"))
	}
}

// Restore is the fresh-clone path. It must import into the declared domain,
// and must not touch an application this Mac has not installed.
func TestRestorePreferenceImportsOnlyWhenTheAppIsInstalled(t *testing.T) {
	paths := testPaths(t)
	machine := testMachine()
	preference := machine.Preferences[0]
	plist := []byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>k</key><true/></dict></plist>`)
	if err := atomicWrite(preference.snapshotPath(paths), plist, 0o600); err != nil {
		t.Fatal(err)
	}
	commands := fakeTools(t, fakeTool{name: "defaults"}, fakeTool{name: "open"}, fakeTool{name: "osascript"})
	applier, chatter := testApplier(t, paths, machine, applyRunner{})

	if err := applier.RestorePreferences(); err != nil {
		t.Fatalf("restore: %v\n%s", err, chatter.String())
	}
	issued := strings.Join(commands(), "\n")
	want := "defaults import " + preference.Domain + " " + preference.snapshotPath(paths)
	if !strings.Contains(issued, want) {
		t.Fatalf("restore did not import the saved domain:\nwant %q\ngot\n%s", want, issued)
	}
	// The stub reports the app as not running, so nothing should be relaunched.
	if strings.Contains(issued, "open ") {
		t.Fatalf("restore relaunched an application that was not running:\n%s", issued)
	}

	uninstalled, chatter := testApplier(t, paths, machine, notInstalledRunner{})
	if err := uninstalled.RestorePreferences(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(chatter.String(), "is not installed") {
		t.Fatalf("an uninstalled application was not reported:\n%s", chatter.String())
	}
}

// notInstalledRunner answers the Spotlight lookup with nothing found.
type notInstalledRunner struct{}

func (notInstalledRunner) Run(context.Context, string, ...string) Result { return Result{} }
func (notInstalledRunner) Exists(string) bool                            { return true }

// sequencedDockRunner reads the Dock differently on each probe, so a test can
// model the live Dock actually changing between apply and its verification.
// Everything else it answers as already converged.
type sequencedDockRunner struct {
	listings []string
	reads    int
}

func (r *sequencedDockRunner) Run(ctx context.Context, name string, args ...string) Result {
	if name == "dockutil" && slices.Equal(args, []string{"--list"}) {
		listing := r.listings[min(r.reads, len(r.listings)-1)]
		r.reads++
		return Result{Stdout: listing}
	}
	// Machine setup converges, so a test of what follows it is not answering
	// for the macOS facts as well.
	return converged{}.Run(ctx, name, args...)
}

func (*sequencedDockRunner) Exists(string) bool { return true }

// Capturing a preference through Apply writes the whole live domain into the
// repository, so a snapshot can carry it to the next machine.
func TestApplyCapturesAPreferenceIntoTheRepository(t *testing.T) {
	paths := testPaths(t)
	machine := testMachine()
	machine.Dock = false
	machine.ChromePWAs = false
	preference := machine.Preferences[0]
	applier, chatter := testApplier(t, paths, machine, applyRunner{})

	if err := applier.Apply([]Selection{{ID: preference.ID, Action: Capture}}); err != nil {
		t.Fatalf("capture preference: %v\n%s", err, chatter.String())
	}
	data, err := os.ReadFile(preference.snapshotPath(paths))
	if err != nil {
		t.Fatalf("capture wrote no backup: %v", err)
	}
	if _, err := decodePlist(data); err != nil {
		t.Fatalf("captured backup is not a plist: %v", err)
	}
	// Apply is the wrong direction for a preference; only Capture writes.
	fresh := testPaths(t)
	other, _ := testApplier(t, fresh, machine, applyRunner{})
	if err := other.Apply([]Selection{{ID: preference.ID, Action: Apply}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(preference.snapshotPath(fresh)); !os.IsNotExist(err) {
		t.Fatalf("Apply wrote a preference backup: %v", err)
	}
}

// One failed step is one failure. The baseline pass runs after every step and
// cannot establish an agreement the failed step never reached, so it must not
// report the same resource a second time.
func TestApplyReportsAFailedStepOnce(t *testing.T) {
	paths := testPaths(t)
	machine := testMachine()
	machine.ChromePWAs = false
	machine.Preferences = nil
	first := paths.InHome("Applications", "First.app")
	if err := os.MkdirAll(first, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(dockSnapshotPath(paths), []byte(first+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &sequencedDockRunner{listings: []string{""}}
	fakeTools(t, fakeTool{name: "dockutil", exit: 1}, fakeTool{name: "killall"})
	applier, chatter := testApplier(t, paths, machine, runner)

	err := applier.Apply([]Selection{{ID: dockID, Action: Apply}})
	if err == nil {
		t.Fatalf("a failed dockutil was not reported:\n%s", chatter.String())
	}
	if got := strings.Count(err.Error(), dockName+":"); got != 1 {
		t.Fatalf("Dock reported %d times in %q", got, err.Error())
	}
}

// Selecting Chrome PWAs through Apply has to reach the capture and restore
// the resource offers. The step table is the only thing routing an action to
// them, and a plan that reaches neither would look like a clean apply.
func TestApplyRoutesChromePWAActions(t *testing.T) {
	paths := testPaths(t)
	machine := testMachine()
	machine.Dock = false
	machine.Preferences = nil
	icon := []byte("icon")
	app := testChromePWA("Gmail", "fmgjjmmmlfnkbppncabfkddbjimcfncm", "https://mail.google.com/", icon)
	writeTestLivePWA(t, paths, app, icon)

	// Capture routes to the backup, which the repository did not have.
	applier, chatter := testApplier(t, paths, machine, OSRunner{Dir: paths.Root})
	if err := applier.Apply([]Selection{{ID: chromePWAsID, Action: Capture}}); err != nil {
		t.Fatalf("capture PWAs: %v\n%s", err, chatter.String())
	}
	saved, apps, hasSaved, err := applier.Bidir.chromePWASaved()
	if err != nil || !hasSaved {
		t.Fatalf("capture wrote no backup: %v", err)
	}
	if len(apps) != 1 || apps[0].Name != "Gmail" {
		t.Fatalf("captured PWAs = %+v", apps)
	}

	// Apply routes to the restore, which has nothing to change here and must
	// say so rather than reporting a failure.
	live, _, _, err := applier.Bidir.chromePWALive()
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != string(live) {
		t.Fatalf("capture did not agree with the live collection")
	}
	if err := applier.Apply([]Selection{{ID: chromePWAsID, Action: Apply}}); err != nil {
		t.Fatalf("apply PWAs: %v\n%s", err, chatter.String())
	}
	if !strings.Contains(chatter.String(), "already current") {
		t.Fatalf("apply did not reach the restore:\n%s", chatter.String())
	}
}

// runningAppRunner reports the application as running until it is asked to
// quit, which is what restorePreference waits for before it imports.
type runningAppRunner struct {
	mu     sync.Mutex
	quit   bool
	probes int
}

func (r *runningAppRunner) Run(_ context.Context, name string, args ...string) Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case name == "mdfind":
		return Result{Stdout: "/Applications/Example.app\n"}
	case name == "osascript":
		r.probes++
		if r.quit {
			return Result{Stdout: "false\n"}
		}
		// The quit is issued through the live runner, so the second probe is
		// the first one after it.
		r.quit = r.probes >= 2
		return Result{Stdout: "true\n"}
	}
	return Result{}
}

func (*runningAppRunner) Exists(string) bool { return true }

// A running application holds its preferences in memory and would write them
// back over the import, so restore quits it first and puts it back after.
func TestRestorePreferenceQuitsAndRelaunchesARunningApp(t *testing.T) {
	paths := testPaths(t)
	machine := testMachine()
	preference := machine.Preferences[0]
	plist := []byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>k</key><true/></dict></plist>`)
	if err := atomicWrite(preference.snapshotPath(paths), plist, 0o600); err != nil {
		t.Fatal(err)
	}
	commands := fakeTools(t, fakeTool{name: "defaults"}, fakeTool{name: "open"}, fakeTool{name: "osascript"})
	applier, chatter := testApplier(t, paths, machine, &runningAppRunner{})

	if err := applier.RestorePreferences(); err != nil {
		t.Fatalf("restore: %v\n%s", err, chatter.String())
	}
	issued := commands()
	var order []string
	for _, command := range issued {
		switch {
		case strings.HasPrefix(command, "osascript"):
			order = append(order, "quit")
		case strings.HasPrefix(command, "defaults import"):
			order = append(order, "import")
		case strings.HasPrefix(command, "open"):
			order = append(order, "relaunch")
		}
	}
	if !slices.Equal(order, []string{"quit", "import", "relaunch"}) {
		t.Fatalf("restore order = %v, want quit then import then relaunch:\n%s", order, strings.Join(issued, "\n"))
	}
}

// An application that will not quit would overwrite the import, so restore
// refuses rather than leaving the saved settings half applied.
func TestRestorePreferenceRefusesAnApplicationThatWillNotQuit(t *testing.T) {
	paths := testPaths(t)
	machine := testMachine()
	preference := machine.Preferences[0]
	plist := []byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>k</key><true/></dict></plist>`)
	if err := atomicWrite(preference.snapshotPath(paths), plist, 0o600); err != nil {
		t.Fatal(err)
	}
	commands := fakeTools(t, fakeTool{name: "defaults"}, fakeTool{name: "open"}, fakeTool{name: "osascript"})
	// Never reports the app as gone.
	stubborn := &runningAppRunner{}
	stubborn.probes = -1000
	applier, _ := testApplier(t, paths, machine, stubborn)
	applier.QuitPoll = time.Millisecond

	err := applier.RestorePreferences()
	if err == nil || !strings.Contains(err.Error(), "did not quit") {
		t.Fatalf("a stubborn application produced %v", err)
	}
	if strings.Contains(strings.Join(commands(), "\n"), "defaults import") {
		t.Fatal("settings were imported over a running application")
	}
}
