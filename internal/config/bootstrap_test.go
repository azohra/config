package config

import (
	"os"
	"strings"
	"testing"
)

// A fresh Mac has no earlier state to fall back on, so one unreadable backup
// must not cost the capabilities beside it. The Dock here is independent of
// the Chrome PWA backup and would restore perfectly well without it.
func TestRestoreFreshKeepsGoingPastAnUnreadableBackup(t *testing.T) {
	paths := testPaths(t)
	machine := testMachine()
	machine.Preferences = nil

	// A PWA manifest Config cannot read.
	if err := atomicWrite(chromePWASnapshotPath(paths), []byte("{not a manifest"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A Dock layout it can.
	app := paths.InHome("Applications", "First.app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(dockSnapshotPath(paths), []byte(app+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	commands := fakeTools(t, fakeTool{name: "mise"}, fakeTool{name: "dockutil"}, fakeTool{name: "killall"})
	// Every Apply marks baselines, so the Dock is read once for setup, once
	// for the restore itself, and once to verify it.
	restored := "First\tfile://" + app + "/\tpersistentApps\n"
	runner := &sequencedDockRunner{listings: []string{"", "", restored}}
	applier, chatter := testApplier(t, paths, machine, runner)

	err := restoreFresh(applier)
	if err == nil {
		t.Fatalf("the unreadable PWA backup was not reported:\n%s", chatter.String())
	}
	if !strings.Contains(err.Error(), chromePWAsName) {
		t.Fatalf("the failure does not name the capability: %v", err)
	}
	issued := strings.Join(commands(), "\n")
	if !strings.Contains(issued, "dockutil --add") {
		t.Fatalf("the Dock never restored past the unreadable PWA backup:\ncommands:[%s]\nlog:%s", issued, chatter.String())
	}
}

// Mise installs the applications every later step restores into, so a failed
// setup is the one place the sequence should stop.
func TestRestoreFreshStopsWhenSetupFails(t *testing.T) {
	paths := testPaths(t)
	machine := testMachine()
	machine.Preferences = nil
	commands := fakeTools(t, fakeTool{name: "mise", exit: 1}, fakeTool{name: "dockutil"})
	applier, _ := testApplier(t, paths, machine, &sequencedDockRunner{listings: []string{""}})

	if err := restoreFresh(applier); err == nil {
		t.Fatal("a failed setup was reported as a successful restore")
	}
	if strings.Contains(strings.Join(commands(), "\n"), "dockutil") {
		t.Fatal("the restore continued after setup failed")
	}
}
