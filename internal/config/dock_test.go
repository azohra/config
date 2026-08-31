package config

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"howett.net/plist"
)

type dockRunner struct {
	state dockState
	err   error
}

func (r dockRunner) Run(_ context.Context, name string, args ...string) Result {
	if name != "defaults" || !slices.Equal(args, []string{"export", dockDomain, "-"}) {
		return Result{}
	}
	if r.err != nil {
		return Result{Stderr: r.err.Error(), Err: r.err}
	}
	values := map[string]any{}
	if r.state.Present {
		values[dockKey] = r.state.Tiles
	}
	data, err := plist.Marshal(values, plist.XMLFormat)
	if err != nil {
		panic(err)
	}
	return Result{Stdout: string(data)}
}

func (dockRunner) Exists(string) bool { return true }

func dockDocument(paths ...string) string {
	tiles := make([]any, 0, len(paths))
	for index, path := range paths {
		tiles = append(tiles, newDockAppTile(path, uint64(1_000_000_000+index)))
	}
	data, err := plist.Marshal(map[string]any{dockKey: tiles}, plist.XMLFormat)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func TestDockStoreReadsOnlyThePersistentAppsKey(t *testing.T) {
	tile := newDockAppTile("/Applications/Example.app", 1_000_000_001)
	runner := dockRunner{state: dockState{Present: true, Tiles: []any{tile}}}
	state, err := (defaultsDockStore{Runner: runner}).Read()
	if err != nil {
		t.Fatal(err)
	}
	if !state.Present || !reflect.DeepEqual(state.Tiles, []any{tile}) {
		t.Fatalf("Dock state = %#v", state)
	}
}

func TestDockStoreWritesOnlyThePersistentAppsKey(t *testing.T) {
	commands := fakeTools(t, fakeTool{name: "defaults"})
	store := defaultsDockStore{Live: newLiveRunner(t.TempDir())}
	if err := store.Write(dockState{Present: true, Tiles: []any{newDockAppTile("/Applications/Example.app", 1_000_000_001)}}); err != nil {
		t.Fatal(err)
	}
	issued := strings.Join(commands(), "\n")
	if !strings.HasPrefix(issued, "defaults write "+dockDomain+" "+dockKey+" ") {
		t.Fatalf("Dock write = %q", issued)
	}
	if strings.Contains(issued, " import ") {
		t.Fatalf("Dock write replaced the whole domain: %q", issued)
	}
}

func TestDockStoreWriteFailureDoesNotPrintThePreferenceValue(t *testing.T) {
	fakeTools(t, fakeTool{name: "defaults", exit: 1})
	store := defaultsDockStore{Live: newLiveRunner(t.TempDir())}
	err := store.Write(dockState{Present: true, Tiles: []any{newDockAppTile("/Applications/Private App.app", 1_000_000_001)}})
	if err == nil || !strings.Contains(err.Error(), "defaults write "+dockDomain+" "+dockKey) {
		t.Fatalf("Dock write error = %v", err)
	}
	for _, leaked := range []string{"Private App", "tile-data", "CFURL"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("Dock write error includes %q: %v", leaked, err)
		}
	}
}

func TestDockStoreDeletesOnlyTheOriginallyMissingKey(t *testing.T) {
	commands := fakeTools(t, fakeTool{name: "defaults"})
	store := defaultsDockStore{Live: newLiveRunner(t.TempDir())}
	if err := store.Write(dockState{}); err != nil {
		t.Fatal(err)
	}
	want := "defaults delete " + dockDomain + " " + dockKey
	if issued := strings.Join(commands(), "\n"); issued != want {
		t.Fatalf("Dock rollback = %q, want %q", issued, want)
	}
}

func TestReconcileDockTilesPreservesOpaqueState(t *testing.T) {
	a := newDockAppTile("/Applications/A.app", 1_000_000_001)
	a["GUID"] = uint64(111)
	b := newDockAppTile("/Applications/B.app", 1_000_000_002)
	b["GUID"] = uint64(222)
	b["tile-data"].(map[string]any)["book"] = []byte("opaque bookmark")
	spacer := map[string]any{"tile-type": "spacer-tile", "opaque": []byte("keep me")}
	original := dockState{Present: true, Tiles: []any{a, spacer, b}}

	updated, err := reconcileDockTiles(original, []string{"/Applications/B.app", "/Applications/C.app"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := dockAppPaths(updated), []string{"/Applications/B.app", "/Applications/C.app"}; !slices.Equal(got, want) {
		t.Fatalf("reconciled apps = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(updated.Tiles[0], b) {
		t.Fatalf("existing app dictionary was rebuilt:\ngot  %#v\nwant %#v", updated.Tiles[0], b)
	}
	if !reflect.DeepEqual(updated.Tiles[1], spacer) {
		t.Fatalf("unmanaged tile changed:\ngot  %#v\nwant %#v", updated.Tiles[1], spacer)
	}
	if path, ok := dockAppPath(updated.Tiles[2]); !ok || path != "/Applications/C.app" {
		t.Fatalf("missing app tile = %#v", updated.Tiles[2])
	}
	guid, ok := dockGUID(updated.Tiles[2])
	if !ok || guid == 1_000_000_001 || guid == 1_000_000_002 {
		t.Fatalf("new app tile GUID = %d, present %t", guid, ok)
	}
}

func TestReconcileDockTilesGivesNewAppsDistinctGUIDs(t *testing.T) {
	updated, err := reconcileDockTiles(dockState{}, []string{"/Applications/A.app", "/Applications/B.app"})
	if err != nil {
		t.Fatal(err)
	}
	first, firstOK := dockGUID(updated.Tiles[0])
	second, secondOK := dockGUID(updated.Tiles[1])
	if !firstOK || !secondOK || first == second {
		t.Fatalf("new Dock GUIDs = %d (%t), %d (%t)", first, firstOK, second, secondOK)
	}
}

func TestDockAppPathRejectsNonAppsAndRemoteURLs(t *testing.T) {
	for _, tile := range []any{
		map[string]any{"tile-type": "spacer-tile"},
		map[string]any{"tile-type": "file-tile", "tile-data": map[string]any{"file-data": map[string]any{"_CFURLString": "file:///Users/me/Downloads/"}}},
		map[string]any{"tile-type": "file-tile", "tile-data": map[string]any{"file-data": map[string]any{"_CFURLString": "https://example.com/Example.app"}}},
	} {
		if path, ok := dockAppPath(tile); ok {
			t.Fatalf("non-app tile decoded as %q: %#v", path, tile)
		}
	}
	plain := map[string]any{"tile-type": "file-tile", "tile-data": map[string]any{"file-data": map[string]any{"_CFURLString": "/Applications/Example.app"}}}
	if path, ok := dockAppPath(plain); !ok || path != "/Applications/Example.app" {
		t.Fatalf("plain file path decoded as %q, present %t", path, ok)
	}
}

func TestDockDiffUsesPlainLanguage(t *testing.T) {
	a := "/Applications/A.app"
	b := "/Applications/B.app"
	c := "/Applications/C.app"
	want := []string{"Only on this Mac: C.app", "Only in the saved layout: A.app"}
	if got := dockDiff([]string{a, b}, []string{b, c}); !slices.Equal(got, want) {
		t.Fatalf("dockDiff() = %#v, want %#v", got, want)
	}
	want = []string{"The same apps are in a different order"}
	if got := dockDiff([]string{a, b}, []string{b, a}); !slices.Equal(got, want) {
		t.Fatalf("dockDiff() = %#v, want %#v", got, want)
	}
}

func TestDockInitialCaptureCreatesTheTrackedSnapshot(t *testing.T) {
	paths := testPaths(t)
	app := paths.InHome("Applications", "Example.app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	bidir := testBidirectional(paths, dockRunner{state: dockState{Present: true, Tiles: []any{newDockAppTile(app, 1_000_000_001)}}})

	resource := bidir.InspectDock()
	if resource.State != Uncaptured || resource.Failed() != 0 || !slices.Equal(resource.Actions, []Action{Capture}) {
		t.Fatalf("uncaptured Dock = %#v", resource)
	}
	if err := bidir.CaptureDock(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dockSnapshotPath(paths))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "~/Applications/Example.app\n" {
		t.Fatalf("captured Dock = %q", data)
	}
	if resource = bidir.InspectDock(); resource.State != Current {
		t.Fatalf("captured Dock resource = %#v", resource)
	}
}

func TestDockInitialCaptureCanTrackAnEmptyLayout(t *testing.T) {
	paths := testPaths(t)
	bidir := testBidirectional(paths, dockRunner{state: dockState{Present: true}})

	if err := bidir.CaptureDock(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dockSnapshotPath(paths))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "\n" {
		t.Fatalf("captured empty Dock = %q", data)
	}
	if resource := bidir.InspectDock(); resource.State != Current {
		t.Fatalf("captured empty Dock resource = %#v", resource)
	}
}

func TestDockCaptureCanAcceptAnUnavailableSavedApp(t *testing.T) {
	paths := testPaths(t)
	if err := atomicWrite(dockSnapshotPath(paths), []byte("/Applications/Missing.app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := paths.InHome("Applications", "Example.app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	bidir := testBidirectional(paths, dockRunner{state: dockState{Present: true, Tiles: []any{newDockAppTile(app, 1_000_000_001)}}})
	resource := bidir.InspectDock()
	if !slices.Equal(resource.Actions, []Action{Capture}) {
		t.Fatalf("Dock with unavailable saved app = %#v", resource)
	}
	if err := bidir.CaptureDock(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dockSnapshotPath(paths))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "~/Applications/Example.app\n" {
		t.Fatalf("recaptured Dock = %q", data)
	}
}

func TestDockReadFailureReachesTheResource(t *testing.T) {
	paths := testPaths(t)
	resource := testBidirectional(paths, dockRunner{err: errors.New("defaults: cannot read the Dock")}).InspectDock()
	if resource.State != Unavailable || resource.Failed() == 0 {
		t.Fatalf("failed Dock read = %#v", resource)
	}
	if detail := resource.Checks[len(resource.Checks)-1].Detail; !strings.Contains(detail, "defaults: cannot read the Dock") {
		t.Fatalf("check detail = %q", detail)
	}
}

func TestDockStoreWritePreservesTileValueTypes(t *testing.T) {
	// defaults reads the value argument as a property list. OpenStep has no
	// integer or boolean type, so encoding tiles that way silently rewrites
	// GUID, file-type, and dock-extra in the user's Dock as strings.
	fakeTools(t, fakeTool{name: "defaults"})
	tile := map[string]any{
		"GUID":      uint64(1_000_000_001),
		"tile-type": "file-tile",
		"tile-data": map[string]any{"dock-extra": false, "file-type": uint64(41)},
	}
	store := defaultsDockStore{Live: newLiveRunner(t.TempDir())}
	if err := store.Write(dockState{Present: true, Tiles: []any{tile}}); err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(os.Getenv("COMMAND_LOG"))
	if err != nil {
		t.Fatal(err)
	}
	_, value, found := strings.Cut(string(logged), dockKey+" ")
	if !found {
		t.Fatalf("Dock write = %q", logged)
	}
	var written []any
	if _, err := plist.Unmarshal([]byte(strings.TrimSpace(value)), &written); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(written, []any{tile}) {
		t.Fatalf("Dock tiles round-tripped as %#v", written)
	}
	if _, ok := dockGUID(written[0]); !ok {
		t.Fatal("the written tile no longer carries a Dock GUID")
	}
}

func TestDockDoesNotWedgeWhenASavedAppLeavesTheDisk(t *testing.T) {
	// The saved layout and the Dock hold the same two apps; one of them was
	// deleted from disk. Reducing only the saved side by existence compared
	// two different lists, reported the saved side as changed, and offered a
	// capture that rewrote the same tiles and could never clear it.
	paths := testPaths(t)
	present := paths.InHome("Applications", "Present.app")
	if err := os.MkdirAll(present, 0o755); err != nil {
		t.Fatal(err)
	}
	const deleted = "/Applications/Deleted.app"
	if err := atomicWrite(dockSnapshotPath(paths),
		[]byte("~/Applications/Present.app\n"+deleted+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bidir := testBidirectional(paths, dockRunner{state: dockState{Present: true, Tiles: []any{
		newDockAppTile(present, 1_000_000_001),
		newDockAppTile(deleted, 1_000_000_002),
	}}})
	live, _, err := bidir.dockLive()
	if err != nil {
		t.Fatal(err)
	}
	if err := bidir.Baselines.Save(dockID, live); err != nil {
		t.Fatal(err)
	}
	resource := bidir.InspectDock()
	if resource.State != Current {
		t.Fatalf("agreeing Dock reported as %q: %#v", resource.State, resource)
	}
	if resource.NeedsDecision() {
		t.Fatalf("Dock asked for a choice it cannot clear: %#v", resource.Actions)
	}
	if resource.Failed() == 0 {
		t.Fatal("the unavailable saved app was not reported")
	}
}
