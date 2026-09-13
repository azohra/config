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

func completedPlatformRestoreSteps(machine Machine) []string {
	var steps []string
	if len(macOSFacts(machine)) > 0 {
		steps = append(steps, restoreMacOSStep)
	}
	if machine.Mise {
		steps = append(steps, restoreMiseStep, restoreMiseDefaultsStep)
	}
	return steps
}

func TestFreshRestoreOmitsUndeclaredMise(t *testing.T) {
	machine := testMachine()
	machine.Mise = false
	for _, step := range freshRestoreSteps(Applier{Machine: machine}) {
		if step.id == restoreMiseStep {
			t.Fatal("an undeclared Mise resource entered the restore plan")
		}
	}
}

// A fresh Mac has no earlier state to fall back on, so one unreadable backup
// must not cost the independent capabilities beside it.
func TestPendingRestoreKeepsGoingPastAnUnreadableBackup(t *testing.T) {
	paths := testPaths(t)
	machine := testMachine()
	machine.Preferences = nil

	// A PWA manifest Config cannot read.
	if err := AtomicWrite(chromePWASnapshotPath(paths), []byte("{not a manifest"), 0o644); err != nil {
		t.Fatal(err)
	}
	commands := fakeTools(t, fakeTool{name: "mise"}, fakeTool{name: "defaults"})
	applier, chatter := testApplier(t, paths, machine, converged{})
	progress := testRestoreProgress(t, paths, machine)

	err := restorePending(applier, progress)
	if err == nil {
		t.Fatalf("the unreadable PWA backup was not reported:\n%s", chatter.String())
	}
	if !strings.Contains(err.Error(), chromePWAsName) {
		t.Fatalf("the failure does not name the capability: %v", err)
	}
	if !progress.done(restoreMacOSStep) || !progress.done(restoreMiseStep) ||
		progress.done("resource/"+chromePWAsID) {
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
		t.Fatalf("retry repeated completed platform work:\nbefore:\n%s\nafter:\n%s", beforeRetry, afterRetry)
	}
}

func TestPendingRestoreContinuesPastMiseFailure(t *testing.T) {
	paths := testPaths(t)
	machine := testMachine()
	machine.Preferences = nil
	commands := fakeTools(t, fakeTool{name: "mise", exit: 1}, fakeTool{name: "defaults"})
	applier, _ := testApplier(t, paths, machine, converged{})
	progress := testRestoreProgress(t, paths, machine)

	if err := restorePending(applier, progress); err == nil {
		t.Fatal("a failed Mise resource was reported as a successful restore")
	}
	if progress.done(restoreMiseStep) || progress.done(restoreMiseDefaultsStep) {
		t.Fatal("a failed Mise resource was recorded as complete")
	}
	if !progress.done(restoreMacOSStep) {
		t.Fatalf("Mise blocked independent resources: %v", progress.record.Completed)
	}
	if strings.Contains(strings.Join(commands(), "\n"), "--only") {
		t.Fatal("defaults or final hooks ran after failed Mise setup")
	}
	if issued := strings.Join(commands(), "\n"); !strings.Contains(issued, "mise bootstrap --yes --skip-dirty --skip macos-defaults,task,final-hook") {
		t.Fatalf("the Mise failure was not exercised:\n%s", issued)
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
	progress.record.Completed = completedPlatformRestoreSteps(machine)
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
