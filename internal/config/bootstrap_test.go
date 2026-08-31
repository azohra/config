package config

import (
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func testRestoreProgress(t *testing.T, paths Paths, machine Machine) *restoreProgress {
	t.Helper()
	const checkout = "0123456789abcdef0123456789abcdef"
	gitTest(t, paths.Root, "init", "--quiet", "--initial-branch=main")
	gitTest(t, paths.Root, "config", "user.name", "Config Test")
	gitTest(t, paths.Root, "config", "user.email", "config@example.invalid")
	declaration, err := toml.Marshal(machine)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(paths.InRoot("config.toml"), declaration, 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, paths.Root, "add", "-A")
	gitTest(t, paths.Root, "commit", "--quiet", "-m", "Add restore fixture")
	gitTest(t, paths.Root, "config", "--local", restoreCheckoutKey, checkout)
	commit, err := cleanCheckoutCommit(paths)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := restorePlanIdentity(paths, machine)
	if err != nil {
		t.Fatal(err)
	}
	progress := &restoreProgress{
		paths: paths,
		record: restoreRecord{
			Schema:     restoreSchema,
			Repository: "github.com/example/machine",
			Checkout:   checkout,
			Commit:     commit,
			Plan:       plan,
			Status:     restorePendingState,
		},
	}
	if err := progress.save(); err != nil {
		t.Fatal(err)
	}
	return progress
}

// A fresh Mac has no earlier state to fall back on, so one unreadable backup
// must not cost the capabilities beside it. The Dock here is independent of
// the Chrome PWA backup and would restore perfectly well without it.
func TestPendingRestoreKeepsGoingPastAnUnreadableBackup(t *testing.T) {
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

	commands := fakeTools(t, fakeTool{name: "mise"}, fakeTool{name: "defaults"}, fakeTool{name: "killall"})
	// Setup records an already-current baseline, then restore reads, verifies,
	// and records the restored agreement explicitly.
	runner := &sequencedDockRunner{listings: []string{dockDocument(), dockDocument(), dockDocument(app)}}
	applier, chatter := testApplier(t, paths, machine, runner)
	progress := testRestoreProgress(t, paths, machine)

	err := restorePending(applier, progress)
	if err == nil {
		t.Fatalf("the unreadable PWA backup was not reported:\n%s", chatter.String())
	}
	if !strings.Contains(err.Error(), chromePWAsName) {
		t.Fatalf("the failure does not name the capability: %v", err)
	}
	issued := strings.Join(commands(), "\n")
	if !strings.Contains(issued, "defaults write "+dockDomain+" "+dockKey) {
		t.Fatalf("the Dock never restored past the unreadable PWA backup:\ncommands:[%s]\nlog:%s", issued, chatter.String())
	}
	if !progress.done(restoreSetupStep) || !progress.done("resource/"+dockID) || progress.done("resource/"+chromePWAsID) {
		t.Fatalf("restore progress after partial failure = %v", progress.record.Completed)
	}
	beforeRetry := strings.Join(commands(), "\n")
	reloaded, pending, err := pendingRestore(paths, machine, io.Discard)
	if err != nil || !pending {
		t.Fatalf("reload restore progress = pending %t, %v", pending, err)
	}
	progress = &reloaded
	if err := restorePending(applier, progress); err == nil {
		t.Fatal("retry hid the unreadable PWA backup")
	}
	if afterRetry := strings.Join(commands(), "\n"); afterRetry != beforeRetry {
		t.Fatalf("retry repeated completed setup or Dock work:\nbefore:\n%s\nafter:\n%s", beforeRetry, afterRetry)
	}
}

// Mise installs the applications every later step restores into, so a failed
// setup is the one place the sequence should stop.
func TestPendingRestoreStopsWhenSetupFails(t *testing.T) {
	paths := testPaths(t)
	machine := testMachine()
	machine.Preferences = nil
	commands := fakeTools(t, fakeTool{name: "mise", exit: 1}, fakeTool{name: "defaults"})
	applier, _ := testApplier(t, paths, machine, &sequencedDockRunner{listings: []string{dockDocument()}})
	progress := testRestoreProgress(t, paths, machine)

	if err := restorePending(applier, progress); err == nil {
		t.Fatal("a failed setup was reported as a successful restore")
	}
	if progress.done(restoreSetupStep) {
		t.Fatal("a failed setup was recorded as complete")
	}
	issued := strings.Join(commands(), "\n")
	if strings.Contains(issued, dockDomain) || strings.Contains(issued, "killall") {
		t.Fatalf("the restore continued into the Dock after setup failed:\n%s", issued)
	}
}

func TestPendingRestoreDoesNotCompleteDockWhenRestartFails(t *testing.T) {
	paths := testPaths(t)
	machine := testMachine()
	machine.ChromePWAs = false
	machine.Preferences = nil
	desired := paths.InHome("Applications", "Desired.app")
	if err := os.MkdirAll(desired, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(dockSnapshotPath(paths), []byte(desired+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	progress := testRestoreProgress(t, paths, machine)
	progress.record.Completed = []string{restoreSetupStep}
	if err := progress.save(); err != nil {
		t.Fatal(err)
	}
	original := dockState{}
	applied := dockState{Present: true, Tiles: []any{newDockAppTile(desired, 1_000_000_003)}}
	store := &failedDockRestart{original: original, applied: applied}
	commands := fakeTools(t, fakeTool{name: "killall", exit: 1})
	applier, _ := testApplier(t, paths, machine, applyRunner{})
	applier.Bidir.Dock = store

	err := restorePending(applier, progress)
	if err == nil || !strings.Contains(err.Error(), "restart Dock") {
		t.Fatalf("restart failure = %v", err)
	}
	if progress.done("resource/" + dockID) {
		t.Fatal("failed Dock restart was recorded as complete")
	}
	if len(store.writes) != 2 || !reflect.DeepEqual(store.writes[1], original) {
		t.Fatalf("Dock rollback writes = %#v", store.writes)
	}
	if issued := commands(); len(issued) != 1 || issued[0] != "killall Dock" {
		t.Fatalf("restart failure commands = %v", issued)
	}
}

func TestPendingRestoreRetriesADockWhoseAppWasInitiallyUnavailable(t *testing.T) {
	paths := testPaths(t)
	machine := testMachine()
	machine.ChromePWAs = false
	machine.Preferences = nil
	desired := paths.InHome("Applications", "Later.app")
	if err := atomicWrite(dockSnapshotPath(paths), []byte(desired+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	progress := testRestoreProgress(t, paths, machine)
	progress.record.Completed = []string{restoreSetupStep}
	if err := progress.save(); err != nil {
		t.Fatal(err)
	}
	store := &memoryDockStore{}
	commands := fakeTools(t, fakeTool{name: "killall"})
	applier, _ := testApplier(t, paths, machine, applyRunner{})
	applier.Bidir.Dock = store

	err := restorePending(applier, progress)
	if err == nil || !strings.Contains(err.Error(), "Later.app is unavailable") {
		t.Fatalf("missing app restore = %v", err)
	}
	if progress.done("resource/"+dockID) || len(store.writes) != 0 || len(commands()) != 0 {
		t.Fatalf("missing app was checkpointed or mutated: completed=%v writes=%v commands=%v", progress.record.Completed, store.writes, commands())
	}

	if err := os.MkdirAll(desired, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := restorePending(applier, progress); err != nil {
		t.Fatalf("retry after app installation: %v", err)
	}
	if !progress.done("resource/"+dockID) || !slices.Equal(dockAppPaths(store.state), []string{desired}) {
		t.Fatalf("retry did not complete Dock restore: completed=%v state=%#v", progress.record.Completed, store.state)
	}
	if issued := commands(); len(issued) != 1 || issued[0] != "killall Dock" {
		t.Fatalf("retry commands = %v", issued)
	}
	extra := paths.InHome("Applications", "Live edit.app")
	store.state = dockState{Present: true, Tiles: []any{
		newDockAppTile(desired, 1_000_000_003),
		newDockAppTile(extra, 1_000_000_004),
	}}
	if resource := applier.Bidir.InspectDock(); resource.State != LiveChanged {
		t.Fatalf("first live edit after restore = %s, want %s", resource.State, LiveChanged)
	}
}

func TestPendingRestoreRestoresFinderFavoritesAndEstablishesABaseline(t *testing.T) {
	paths := testPaths(t)
	machine := testMachine()
	machine.FinderFavorites = true
	machine.ChromePWAs = false
	machine.Dock = false
	machine.Preferences = nil
	first := favoriteDir(t, paths, "First")
	second := favoriteDir(t, paths, "Second")
	writeFinderFavoritesSnapshot(t, paths, []finderFavoriteSnapshotItem{
		snapshotFinderFavorite(paths, finderFavorite{Name: "First", Path: first}),
		snapshotFinderFavorite(paths, finderFavorite{Name: "Second", Path: second}),
	})
	progress := testRestoreProgress(t, paths, machine)
	progress.record.Completed = []string{restoreSetupStep}
	if err := progress.save(); err != nil {
		t.Fatal(err)
	}
	store := &fakeFinderFavorites{}
	applier, _ := testApplier(t, paths, machine, converged{})
	applier.FinderFavorites = store

	if err := restorePending(applier, progress); err != nil {
		t.Fatal(err)
	}
	if !progress.done("resource/" + finderFavoritesID) {
		t.Fatalf("Finder Favorites restore was not checkpointed: %v", progress.record.Completed)
	}
	_, actual, _, err := finderFavoritesLive(store)
	if err != nil {
		t.Fatal(err)
	}
	want := []finderFavorite{{Name: "First", Path: first}, {Name: "Second", Path: second}}
	if !slices.Equal(actual, want) {
		t.Fatalf("restored Favorites = %+v, want %+v", actual, want)
	}

	liveEdit := favoriteDir(t, paths, "Live edit")
	store.items = append(store.items, finderFavoriteItem{ID: 3, Name: "Live edit", Path: liveEdit})
	if resource := applier.Bidir.InspectFinderFavorites(store); resource.State != LiveChanged {
		t.Fatalf("first live edit after restore = %s, want %s", resource.State, LiveChanged)
	}
}
