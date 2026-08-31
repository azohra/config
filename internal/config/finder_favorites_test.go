package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type fakeFinderFavorites struct {
	items        []finderFavoriteItem
	nextID       uint32
	listErr      error
	misplacePuts int
	removeErr    error
	removeErrors int
	operations   []string
}

func (f *fakeFinderFavorites) List() ([]finderFavoriteItem, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return slices.Clone(f.items), nil
}

func (f *fakeFinderFavorites) PutAfter(name, path string, after *finderFavoriteItem) (finderFavoriteItem, error) {
	f.operations = append(f.operations, "put:"+name)
	if f.misplacePuts > 0 {
		f.misplacePuts--
		after = nil
	}
	path = filepath.Clean(path)
	var item finderFavoriteItem
	found := -1
	for index, current := range f.items {
		if filepath.Clean(current.Path) == path {
			item = current
			found = index
			break
		}
	}
	if found >= 0 {
		f.items = append(f.items[:found], f.items[found+1:]...)
	} else {
		f.nextID++
		item.ID = f.nextID
	}
	item.Name = name
	item.Path = path
	position := 0
	if after != nil {
		position = -1
		for index, current := range f.items {
			if current == *after {
				position = index + 1
				break
			}
		}
		if position < 0 {
			return finderFavoriteItem{}, errors.New("anchor changed")
		}
	}
	f.items = append(f.items, finderFavoriteItem{})
	copy(f.items[position+1:], f.items[position:])
	f.items[position] = item
	return item, nil
}

func (f *fakeFinderFavorites) Remove(expected finderFavoriteItem) error {
	f.operations = append(f.operations, "remove:"+expected.Name)
	if f.removeErr != nil && f.removeErrors != 0 {
		if f.removeErrors > 0 {
			f.removeErrors--
		}
		return f.removeErr
	}
	for index, item := range f.items {
		if item.ID != expected.ID {
			continue
		}
		if item != expected {
			return errors.New("favorite changed")
		}
		f.items = append(f.items[:index], f.items[index+1:]...)
		return nil
	}
	return errors.New("favorite disappeared")
}

func favoriteDir(t *testing.T, paths Paths, name string) string {
	t.Helper()
	path := filepath.Join(paths.Home, name)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeFinderFavoritesSnapshot(t *testing.T, paths Paths, favorites []finderFavoriteSnapshotItem) {
	t.Helper()
	snapshot := finderFavoriteSnapshot{Schema: finderFavoritesSnapshotSchema, Favorites: favorites}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(finderFavoritesSnapshotPath(paths), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func finderFavoritesApplier(paths Paths, store finderFavoritesStore) (Applier, *bytes.Buffer) {
	var output bytes.Buffer
	machine := testMachine()
	machine.FinderFavorites = true
	return Applier{
		Paths: paths, Machine: machine, FinderFavorites: store,
		Log: Logger{Out: &output}, Bidir: testBidirectional(paths, converged{}),
	}, &output
}

func TestApplyDispatchesFinderFavoritesCapture(t *testing.T) {
	paths := testPaths(t)
	target := favoriteDir(t, paths, "Target")
	store := &fakeFinderFavorites{items: []finderFavoriteItem{{ID: 1, Name: "Target", Path: target}}}
	applier, _ := finderFavoritesApplier(paths, store)

	if err := applier.Apply([]Selection{{ID: finderFavoritesID, Action: Capture}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(finderFavoritesSnapshotPath(paths)); err != nil {
		t.Fatalf("selected Finder Favorites capture wrote no snapshot: %v", err)
	}
	if resource := applier.Bidir.InspectFinderFavorites(store); resource.State != Current {
		t.Fatalf("captured Finder Favorites = %+v", resource)
	}
}

func TestFinderFavoritesCaptureUsesPortableTargets(t *testing.T) {
	paths := testPaths(t)
	development := favoriteDir(t, paths, "Development")
	store := &fakeFinderFavorites{items: []finderFavoriteItem{
		{ID: 1, Name: "mac.config", Path: paths.Root},
		{ID: 2, Name: "Development", Path: development},
		{ID: 4, Name: "Home", Path: paths.Home},
		{ID: 3, Name: "Finder owned", Path: ""},
	}}
	bidir := testBidirectional(paths, converged{})
	if err := bidir.CaptureFinderFavorites(store); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(finderFavoritesSnapshotPath(paths))
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "schema": 1,
  "favorites": [
    {
      "name": "mac.config",
      "target": "managed-repository"
    },
    {
      "name": "Development",
      "path": "~/Development"
    },
    {
      "name": "Home",
      "path": "~"
    }
  ]
}
`
	if string(data) != want {
		t.Fatalf("snapshot = %s", data)
	}
	resource := bidir.InspectFinderFavorites(store)
	if resource.State != Current || len(resource.Actions) != 0 {
		t.Fatalf("resource = %+v", resource)
	}
}

func TestFinderFavoritesInspectionClassifiesOrderChanges(t *testing.T) {
	paths := testPaths(t)
	first := favoriteDir(t, paths, "First")
	second := favoriteDir(t, paths, "Second")
	store := &fakeFinderFavorites{items: []finderFavoriteItem{
		{ID: 1, Name: "First", Path: first},
		{ID: 2, Name: "Second", Path: second},
	}}
	bidir := testBidirectional(paths, converged{})
	if resource := bidir.InspectFinderFavorites(store); resource.State != Uncaptured || !slices.Equal(resource.Actions, []Action{Capture}) {
		t.Fatalf("uncaptured resource = %+v", resource)
	}
	if err := bidir.CaptureFinderFavorites(store); err != nil {
		t.Fatal(err)
	}
	if err := bidir.MarkFinderFavoritesIfCurrent(store); err != nil {
		t.Fatal(err)
	}
	store.items[0], store.items[1] = store.items[1], store.items[0]
	resource := bidir.InspectFinderFavorites(store)
	if resource.State != LiveChanged || !slices.Equal(resource.Actions, []Action{Capture, Apply}) ||
		!slices.Contains(resource.Details, "The same Favorites are in a different order") {
		t.Fatalf("resource = %+v", resource)
	}
}

func TestFinderFavoritesInspectionReportsAnUnreadableNativeList(t *testing.T) {
	resource := testBidirectional(testPaths(t), converged{}).InspectFinderFavorites(
		&fakeFinderFavorites{listErr: errors.New("Finder unavailable")},
	)
	if resource.State != Unavailable || resource.Failed() != 1 || len(resource.Actions) != 0 {
		t.Fatalf("resource = %+v", resource)
	}
}

func TestApplyFinderFavoritesRestoresSetNamesAndOrderAroundOpaqueItems(t *testing.T) {
	paths := testPaths(t)
	first := favoriteDir(t, paths, "First")
	second := favoriteDir(t, paths, "Second")
	extra := favoriteDir(t, paths, "Extra")
	store := &fakeFinderFavorites{nextID: 4, items: []finderFavoriteItem{
		{ID: 3, Name: "Finder owned first", Path: ""},
		{ID: 2, Name: "Saved Second", Path: second},
		{ID: 4, Name: "Finder owned second", Path: ""},
		{ID: 1, Name: "First", Path: first},
	}}
	bidir := testBidirectional(paths, converged{})
	if err := bidir.CaptureFinderFavorites(store); err != nil {
		t.Fatal(err)
	}
	store.items = []finderFavoriteItem{
		{ID: 3, Name: "Finder owned first", Path: ""},
		{ID: 1, Name: "First", Path: first},
		{ID: 5, Name: "Extra", Path: extra},
		{ID: 2, Name: "Live Second", Path: second},
		{ID: 4, Name: "Finder owned second", Path: ""},
	}
	applier, output := finderFavoritesApplier(paths, store)
	if err := applier.reconcileFinderFavorites(Apply); err != nil {
		t.Fatal(err)
	}
	_, actual, opaque, err := finderFavoritesLive(store)
	if err != nil {
		t.Fatal(err)
	}
	want := []finderFavorite{{Name: "Saved Second", Path: second}, {Name: "First", Path: first}}
	if !slices.Equal(actual, want) {
		t.Fatalf("Favorites = %+v", actual)
	}
	if got := []uint32{store.items[0].ID, store.items[1].ID, store.items[2].ID, store.items[3].ID}; !slices.Equal(got, []uint32{3, 2, 1, 4}) {
		t.Fatalf("full Finder order = %+v", store.items)
	}
	if got := []uint32{opaque[0].ID, opaque[1].ID}; !slices.Equal(got, []uint32{3, 4}) {
		t.Fatalf("opaque items = %+v", opaque)
	}
	if !strings.Contains(output.String(), "saved Favorites restored") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestApplyEmptyFinderFavoritesSnapshotRemovesOnlyManagedEntries(t *testing.T) {
	paths := testPaths(t)
	first := favoriteDir(t, paths, "First")
	second := favoriteDir(t, paths, "Second")
	opaque := finderFavoriteItem{ID: 2, Name: "Finder owned", Path: ""}
	writeFinderFavoritesSnapshot(t, paths, []finderFavoriteSnapshotItem{})
	store := &fakeFinderFavorites{items: []finderFavoriteItem{
		{ID: 1, Name: "First", Path: first},
		opaque,
		{ID: 3, Name: "Second", Path: second},
	}}
	applier, _ := finderFavoritesApplier(paths, store)

	if err := applier.reconcileFinderFavorites(Apply); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(store.items, []finderFavoriteItem{opaque}) {
		t.Fatalf("Finder items = %+v", store.items)
	}
}

func TestApplyFinderFavoritesLeavesFinderUntouchedWhenATargetIsMissing(t *testing.T) {
	paths := testPaths(t)
	missing := filepath.Join(paths.Home, "Missing")
	writeFinderFavoritesSnapshot(t, paths, []finderFavoriteSnapshotItem{{Name: "Missing", Path: missing}})
	store := &fakeFinderFavorites{}
	applier, _ := finderFavoritesApplier(paths, store)
	err := applier.reconcileFinderFavorites(Apply)
	var advisory advisoryError
	if !errors.As(err, &advisory) || len(store.operations) != 0 {
		t.Fatalf("error = %v, operations = %v", err, store.operations)
	}
}

func TestApplyFinderFavoritesRestoresTheOriginalListAfterFailure(t *testing.T) {
	paths := testPaths(t)
	first := favoriteDir(t, paths, "First")
	second := favoriteDir(t, paths, "Second")
	store := &fakeFinderFavorites{nextID: 2, items: []finderFavoriteItem{{ID: 1, Name: "First", Path: first}}}
	bidir := testBidirectional(paths, converged{})
	store.items = []finderFavoriteItem{{ID: 2, Name: "Second", Path: second}}
	if err := bidir.CaptureFinderFavorites(store); err != nil {
		t.Fatal(err)
	}
	store.items = []finderFavoriteItem{{ID: 1, Name: "First", Path: first}}
	store.removeErr = errors.New("Finder refused the change")
	store.removeErrors = 1
	applier, _ := finderFavoritesApplier(paths, store)
	err := applier.reconcileFinderFavorites(Apply)
	if err == nil || !strings.Contains(err.Error(), "original list restored") {
		t.Fatalf("error = %v", err)
	}
	_, actual, _, listErr := finderFavoritesLive(store)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if !slices.Equal(actual, []finderFavorite{{Name: "First", Path: first}}) {
		t.Fatalf("Favorites = %+v", actual)
	}
}

func TestApplyFinderFavoritesRejectsWrongNativePlacementAndRestoresTheLayout(t *testing.T) {
	paths := testPaths(t)
	first := favoriteDir(t, paths, "First")
	second := favoriteDir(t, paths, "Second")
	opaque := finderFavoriteItem{ID: 1, Name: "Finder owned", Path: ""}
	originalItems := []finderFavoriteItem{opaque, {ID: 2, Name: "First", Path: first}}
	originalLayout, err := finderFavoritesLayout(originalItems)
	if err != nil {
		t.Fatal(err)
	}
	writeFinderFavoritesSnapshot(t, paths, []finderFavoriteSnapshotItem{{Name: "Second", Path: second}})
	store := &fakeFinderFavorites{items: slices.Clone(originalItems), nextID: 2, misplacePuts: 1}
	applier, _ := finderFavoritesApplier(paths, store)

	err = applier.reconcileFinderFavorites(Apply)
	if err == nil || !strings.Contains(err.Error(), "original list restored") {
		t.Fatalf("wrong placement result = %v", err)
	}
	restored, layoutErr := finderFavoritesLayout(store.items)
	if layoutErr != nil {
		t.Fatal(layoutErr)
	}
	if !slices.Equal(restored, originalLayout) {
		t.Fatalf("restored layout = %+v, want %+v", restored, originalLayout)
	}
}

func TestFinderFavoritesRejectInvalidSnapshotsAndLiveDuplicates(t *testing.T) {
	paths := testPaths(t)
	target := favoriteDir(t, paths, "Target")
	bidir := testBidirectional(paths, converged{})
	for _, invalid := range []string{
		`{"schema":1,"favorites":[{"name":"Target","path":"Target"}]}`,
		`{"schema":1,"favorites":[{"name":"Target","path":"~/../Target"}]}`,
		`{"schema":1,"favorites":[{"name":"Target","target":"unknown"}]}`,
		`{"schema":1,"favorites":[{"name":"Target","path":"/tmp","extra":true}]}`,
	} {
		if err := atomicWrite(finderFavoritesSnapshotPath(paths), []byte(invalid), 0o644); err != nil {
			t.Fatal(err)
		}
		if resource := bidir.InspectFinderFavorites(&fakeFinderFavorites{}); resource.State != Unavailable {
			t.Fatalf("invalid snapshot %s resource = %+v", invalid, resource)
		}
	}
	store := &fakeFinderFavorites{items: []finderFavoriteItem{
		{ID: 1, Name: "Target", Path: target},
		{ID: 2, Name: "Duplicate", Path: target},
	}}
	if err := os.Remove(finderFavoritesSnapshotPath(paths)); err != nil {
		t.Fatal(err)
	}
	if resource := bidir.InspectFinderFavorites(store); resource.State != Unavailable {
		t.Fatalf("duplicate live resource = %+v", resource)
	}
}
