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

// converged answers every macOSFacts probe as already-correct, so prepareMise
// reaches its one live command and stops: nothing to fix, no restarts.
type converged struct{}

func (converged) Run(_ context.Context, name string, args ...string) Result {
	switch {
	case name == "mise" && slices.Equal(args, []string{"--version"}):
		return Result{Stdout: testedMiseVersion}
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
		Paths:    testPaths(t),
		Machine:  testMachine(),
		Mise:     converged{},
		MiseLive: LiveRunner{Stdout: &chatter, Stderr: &chatter},
		Log:      Logger{Out: &chatter},
	}
	if err := applier.prepareMise(); err != nil {
		t.Fatalf("a dirty checkout blocked apply: %v", err)
	}
	commands, err := os.ReadFile(commandLog)
	if err != nil {
		t.Fatal(err)
	}
	want := "bootstrap --yes --skip-dirty --skip macos-defaults,task,final-hook\n"
	if string(commands) != want {
		t.Fatalf("mise order = %q, want %q", commands, want)
	}
}

func TestApplyMiseRefusesAnUnsupportedVersionBeforeMutation(t *testing.T) {
	commands := fakeTools(t, fakeTool{name: "mise"})
	unsupported := unsupportedMiseVersions()[1]
	applier := Applier{
		Mise:     &miseStubRunner{version: unsupported},
		MiseLive: LiveRunner{},
		Log:      Logger{Out: &bytes.Buffer{}},
	}

	err := applier.prepareMise()
	if err == nil || !strings.Contains(err.Error(), unsupported+" is unsupported") {
		t.Fatalf("prepareMise() error = %v, want unsupported mise version", err)
	}
	if issued := commands(); len(issued) != 0 {
		t.Fatalf("unsupported mise executed mutations: %v", issued)
	}
}

// applyRunner answers the probes an Applier makes while reconciling, so a
// test can drive apply without a real Mac.
type applyRunner struct{}

func (r applyRunner) Run(_ context.Context, name string, args ...string) Result {
	switch {
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

func TestRestorePreferenceImportsOnlyWhenTheAppIsInstalled(t *testing.T) {
	paths := testPaths(t)
	machine := testMachine()
	preference := machine.Preferences[0]
	plist := []byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>k</key><true/></dict></plist>`)
	if err := AtomicWrite(preference.snapshotPath(paths), plist, 0o600); err != nil {
		t.Fatal(err)
	}
	commands := fakeTools(t, fakeTool{name: "defaults"}, fakeTool{name: "open"}, fakeTool{name: "osascript"})
	applier, chatter := testApplier(t, paths, machine, applyRunner{})

	if err := applier.restorePreference(preference); err != nil {
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

	uninstalled, _ := testApplier(t, paths, machine, notInstalledRunner{})
	if err := uninstalled.restorePreference(preference); err == nil || !strings.Contains(err.Error(), "restore remains pending") {
		t.Fatalf("missing app restore = %v", err)
	}
}

// notInstalledRunner answers the Spotlight lookup with nothing found.
type notInstalledRunner struct{}

func (notInstalledRunner) Run(context.Context, string, ...string) Result { return Result{} }
func (notInstalledRunner) Exists(string) bool                            { return true }

// Capturing a preference through Apply writes the whole live domain into the
// repository, so a snapshot can carry it to the next machine.
func TestApplyCapturesAPreferenceIntoTheRepository(t *testing.T) {
	paths := testPaths(t)
	machine := testMachine()
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

// Selecting Chrome PWAs through Apply has to reach the capture and restore
// the resource offers. The step table is the only thing routing an action to
// them, and a plan that reaches neither would look like a clean apply.
func TestApplyRoutesChromePWAActions(t *testing.T) {
	paths := testPaths(t)
	machine := testMachine()
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
	if err := AtomicWrite(preference.snapshotPath(paths), plist, 0o600); err != nil {
		t.Fatal(err)
	}
	commands := fakeTools(t, fakeTool{name: "defaults"}, fakeTool{name: "open"}, fakeTool{name: "osascript"})
	applier, chatter := testApplier(t, paths, machine, &runningAppRunner{})

	if err := applier.restorePreference(preference); err != nil {
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
	if err := AtomicWrite(preference.snapshotPath(paths), plist, 0o600); err != nil {
		t.Fatal(err)
	}
	commands := fakeTools(t, fakeTool{name: "defaults"}, fakeTool{name: "open"}, fakeTool{name: "osascript"})
	// Never reports the app as gone.
	stubborn := &runningAppRunner{}
	stubborn.probes = -1000
	applier, _ := testApplier(t, paths, machine, stubborn)
	applier.QuitPoll = time.Millisecond

	err := applier.restorePreference(preference)
	if err == nil || !strings.Contains(err.Error(), "did not quit") {
		t.Fatalf("a stubborn application produced %v", err)
	}
	if strings.Contains(strings.Join(commands(), "\n"), "defaults import") {
		t.Fatal("settings were imported over a running application")
	}
}

func TestPreferenceRelaunchSurvivesAKillAfterTheQuit(t *testing.T) {
	// Whether to relaunch is a fact about the Mac before the quit, and the
	// quit destroys it. A run killed after quitting left the application
	// closed, and the next restore, seeing it closed, never opened it again.
	paths := testPaths(t)
	machine := testMachine()
	preference := machine.Preferences[0]
	plist := []byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>k</key><true/></dict></plist>`)
	if err := AtomicWrite(preference.snapshotPath(paths), plist, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setMarker(paths, relaunchMarker(preference.Bundle)); err != nil {
		t.Fatal(err)
	}
	commands := fakeTools(t, fakeTool{name: "defaults"}, fakeTool{name: "open"}, fakeTool{name: "osascript"})
	// quit: true means the application is already closed, which is what the
	// killed run left behind.
	applier, chatter := testApplier(t, paths, machine, &runningAppRunner{quit: true})

	if err := applier.restorePreference(preference); err != nil {
		t.Fatalf("restore: %v\n%s", err, chatter.String())
	}
	issued := strings.Join(commands(), "\n")
	if !strings.Contains(issued, "open -b "+preference.Bundle) {
		t.Fatalf("the application was left closed:\n%s", issued)
	}
	if strings.Contains(issued, "osascript") {
		t.Fatalf("an application that was already closed was quit again:\n%s", issued)
	}
	if markerSet(paths, relaunchMarker(preference.Bundle)) {
		t.Fatal("the relaunch marker outlived the relaunch")
	}
}

func TestApplyDoesNotFinishMiseAfterPreparationFails(t *testing.T) {
	paths := testPaths(t)
	machine := testMachine()
	commands := fakeTools(t, fakeTool{name: "mise", exit: 1})
	applier, _ := testApplier(t, paths, machine, converged{})
	if err := applier.Apply([]Selection{{ID: miseID, Action: Apply}}); err == nil {
		t.Fatal("Mise setup failure was hidden")
	}
	if issued := commands(); len(issued) != 1 || strings.Contains(issued[0], "--only") {
		t.Fatalf("failed setup reached defaults or final hooks: %v", issued)
	}
}
