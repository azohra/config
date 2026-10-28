package config

import (
	"io"
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
	if err := AtomicWrite(paths.InRoot("config.toml"), declaration, 0o600); err != nil {
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
// must not cost the independent capabilities beside it.
func TestPendingRestoreKeepsGoingPastAnUnreadableBackup(t *testing.T) {
	paths := testPaths(t)
	machine := testMachine()
	machine.Preferences = nil
	machine.FinderFavorites = true
	favorite := favoriteDir(t, paths, "Independent")
	writeFinderFavoritesSnapshot(t, paths, []finderFavoriteSnapshotItem{
		snapshotFinderFavorite(paths, finderFavorite{Name: "Independent", Path: favorite}),
	})

	// A PWA manifest Config cannot read.
	if err := AtomicWrite(chromePWASnapshotPath(paths), []byte("{not a manifest"), 0o644); err != nil {
		t.Fatal(err)
	}
	commands := fakeTools(t, fakeTool{name: "mise"}, fakeTool{name: "defaults"})
	applier, chatter := testApplier(t, paths, machine, converged{})
	store := &fakeFinderFavorites{}
	applier.FinderFavorites = store
	progress := testRestoreProgress(t, paths, machine)

	err := restorePending(applier, progress)
	if err == nil {
		t.Fatalf("the unreadable PWA backup was not reported:\n%s", chatter.String())
	}
	if !strings.Contains(err.Error(), chromePWAsName) {
		t.Fatalf("the failure does not name the capability: %v", err)
	}
	if !progress.done("resource/"+finderFavoritesID) ||
		progress.done("resource/"+chromePWAsID) {
		t.Fatalf("restore progress after partial failure = %v", progress.record.Completed)
	}
	beforeRetry := strings.Join(commands(), "\n")
	operations := slices.Clone(store.operations)
	reloaded, pending, err := pendingRestore(paths, machine, io.Discard)
	if err != nil || !pending {
		t.Fatalf("reload restore progress = pending %t, %v", pending, err)
	}
	progress = &reloaded
	if err := restorePending(applier, progress); err == nil {
		t.Fatal("retry hid the unreadable PWA backup")
	}
	if !slices.Equal(store.operations, operations) {
		t.Fatalf("retry repeated completed Finder restoration: %v", store.operations)
	}
	if afterRetry := strings.Join(commands(), "\n"); afterRetry != beforeRetry {
		t.Fatalf("retry repeated completed restoration:\nbefore:\n%s\nafter:\n%s", beforeRetry, afterRetry)
	}
}

func TestPendingRestoreRestoresFinderFavoritesAndEstablishesABaseline(t *testing.T) {
	paths := testPaths(t)
	machine := testMachine()
	machine.FinderFavorites = true
	machine.ChromePWAs = false
	machine.Preferences = nil
	first := favoriteDir(t, paths, "First")
	second := favoriteDir(t, paths, "Second")
	writeFinderFavoritesSnapshot(t, paths, []finderFavoriteSnapshotItem{
		snapshotFinderFavorite(paths, finderFavorite{Name: "First", Path: first}),
		snapshotFinderFavorite(paths, finderFavorite{Name: "Second", Path: second}),
	})
	progress := testRestoreProgress(t, paths, machine)
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
