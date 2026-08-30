package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type fakeFinderFavorites struct {
	items        []finderFavoriteItem
	listErr      error
	addErr       error
	removeErr    error
	ignoreAdd    bool
	nextID       uint32
	listCalls    int
	beforeList   func(*fakeFinderFavorites)
	beforeRemove func(*fakeFinderFavorites, finderFavoriteItem)
	operations   []string
}

func (f *fakeFinderFavorites) List() ([]finderFavoriteItem, error) {
	f.listCalls++
	if f.beforeList != nil {
		f.beforeList(f)
	}
	if f.listErr != nil {
		return nil, f.listErr
	}
	return slices.Clone(f.items), nil
}

func (f *fakeFinderFavorites) Add(name, path string) error {
	f.operations = append(f.operations, "add")
	if f.addErr != nil {
		return f.addErr
	}
	if !f.ignoreAdd {
		f.nextID++
		f.items = append(f.items, finderFavoriteItem{ID: f.nextID, Name: name, Path: path})
	}
	return nil
}

func (f *fakeFinderFavorites) Remove(expected finderFavoriteItem) error {
	f.operations = append(f.operations, fmt.Sprintf("remove:%d", expected.ID))
	if f.beforeRemove != nil {
		f.beforeRemove(f, expected)
	}
	if f.removeErr != nil {
		return f.removeErr
	}
	for index, item := range f.items {
		if item.ID == expected.ID {
			if item != expected {
				return fmt.Errorf("item %d changed", expected.ID)
			}
			f.items = slices.Delete(f.items, index, index+1)
			return nil
		}
	}
	return fmt.Errorf("item %d is absent", expected.ID)
}

func TestFinderFavoriteValidatesOnlyNativeLabelConstraints(t *testing.T) {
	for _, name := range []string{"all", "Work -> current"} {
		if err := (FinderFavorite{Name: name}).Validate(); err != nil {
			t.Fatalf("Validate(%q): %v", name, err)
		}
	}
	for _, name := range []string{"", " padded", "padded ", "line\nbreak"} {
		if err := (FinderFavorite{Name: name}).Validate(); err == nil {
			t.Fatalf("Validate(%q) succeeded", name)
		}
	}
}

func TestFinderFavoriteInspectionOwnsManagedTarget(t *testing.T) {
	paths := testPaths(t)
	declaration := FinderFavorite{Name: "Machine config"}
	want := finderFavoriteTarget(paths)
	tests := []struct {
		name    string
		store   *fakeFinderFavorites
		state   State
		actions []Action
	}{
		{"favorite absent", &fakeFinderFavorites{}, Drift, []Action{Apply}},
		{"wrong target", &fakeFinderFavorites{items: []finderFavoriteItem{{ID: 1, Name: declaration.Name, Path: "/tmp/elsewhere"}}}, Drift, []Action{Apply}},
		{"duplicate name", &fakeFinderFavorites{items: []finderFavoriteItem{{ID: 1, Name: declaration.Name, Path: want}, {ID: 2, Name: declaration.Name, Path: "/tmp/elsewhere"}}}, Drift, []Action{Apply}},
		{"current", &fakeFinderFavorites{items: []finderFavoriteItem{{ID: 1, Name: declaration.Name, Path: want}}}, Current, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resource := InspectFinderFavorite(paths, declaration, test.store)
			if resource.State != test.state || !slices.Equal(resource.Actions, test.actions) {
				t.Fatalf("resource = %+v", resource)
			}
			if !resource.Authoritative {
				t.Fatal("Finder Favorite must not gate repository snapshots")
			}
		})
	}
}

func TestFinderFavoriteInspectionIsReadOnly(t *testing.T) {
	store := &fakeFinderFavorites{listErr: errors.New("unavailable")}
	resource := InspectFinderFavorite(testPaths(t), FinderFavorite{Name: "Machine config"}, store)
	if resource.State != Unavailable || resource.Failed() != 1 || len(resource.Actions) != 0 {
		t.Fatalf("resource = %+v", resource)
	}
	if len(store.operations) != 0 {
		t.Fatalf("inspection performed writes: %v", store.operations)
	}
}

func TestApplyFinderFavoriteAddsBeforeRemovingStaleItems(t *testing.T) {
	paths := testPaths(t)
	name := "Machine config"
	store := &fakeFinderFavorites{
		nextID: 2,
		items: []finderFavoriteItem{
			{ID: 1, Name: name, Path: "/tmp/first"},
			{ID: 2, Name: name, Path: "/tmp/second"},
		},
	}
	applier, output := finderFavoriteApplier(paths, name, store)
	if err := applier.reconcileFinderFavorite(Apply); err != nil {
		t.Fatal(err)
	}
	wantOperations := []string{"add", "remove:1", "remove:2"}
	if !slices.Equal(store.operations, wantOperations) {
		t.Fatalf("operations = %v, want %v", store.operations, wantOperations)
	}
	if !finderFavoriteCurrent(store.items, name, finderFavoriteTarget(paths)) {
		t.Fatalf("items = %+v", store.items)
	}
	if !bytes.Contains(output.Bytes(), []byte("Finder Favorite added")) {
		t.Fatalf("output = %q", output.String())
	}
}

func TestApplyFinderFavoriteKeepsExistingDesiredItemWhileRemovingDuplicates(t *testing.T) {
	paths := testPaths(t)
	name := "Machine config"
	target := finderFavoriteTarget(paths)
	store := &fakeFinderFavorites{items: []finderFavoriteItem{
		{ID: 1, Name: name, Path: target},
		{ID: 2, Name: name, Path: target},
		{ID: 3, Name: name, Path: "/tmp/stale"},
	}}
	applier, _ := finderFavoriteApplier(paths, name, store)
	if err := applier.reconcileFinderFavorite(Apply); err != nil {
		t.Fatal(err)
	}
	wantOperations := []string{"remove:2", "remove:3"}
	if !slices.Equal(store.operations, wantOperations) {
		t.Fatalf("operations = %v, want %v", store.operations, wantOperations)
	}
}

func TestApplyFinderFavoriteDoesNotRemoveWithoutAConfirmedReplacement(t *testing.T) {
	paths := testPaths(t)
	name := "Machine config"
	store := &fakeFinderFavorites{
		items:     []finderFavoriteItem{{ID: 1, Name: name, Path: "/tmp/stale"}},
		ignoreAdd: true,
	}
	applier, _ := finderFavoriteApplier(paths, name, store)
	if err := applier.reconcileFinderFavorite(Apply); err == nil {
		t.Fatal("apply succeeded without adding the desired Favorite")
	}
	if !slices.Equal(store.operations, []string{"add"}) {
		t.Fatalf("operations = %v", store.operations)
	}
	if len(store.items) != 1 || store.items[0].ID != 1 {
		t.Fatalf("original item was removed: %+v", store.items)
	}
}

func TestApplyFinderFavoriteKeepsTheReplacementWhenCleanupFails(t *testing.T) {
	paths := testPaths(t)
	name := "Machine config"
	store := &fakeFinderFavorites{
		nextID:    1,
		items:     []finderFavoriteItem{{ID: 1, Name: name, Path: "/tmp/stale"}},
		removeErr: errors.New("Finder refused the change"),
	}
	applier, _ := finderFavoriteApplier(paths, name, store)
	if err := applier.reconcileFinderFavorite(Apply); err == nil {
		t.Fatal("apply succeeded without exactly one current Favorite")
	}
	if !slices.Equal(store.operations, []string{"add", "remove:1"}) {
		t.Fatalf("operations = %v", store.operations)
	}
	if _, present := desiredFinderFavorite(store.items, finderFavoriteTarget(paths)); !present {
		t.Fatalf("replacement was lost: %+v", store.items)
	}
}

func TestApplyFinderFavoriteRereadsTheExactPostcondition(t *testing.T) {
	paths := testPaths(t)
	name := "Machine config"
	target := finderFavoriteTarget(paths)
	store := &fakeFinderFavorites{
		nextID: 1,
		items:  []finderFavoriteItem{{ID: 1, Name: name, Path: "/tmp/stale"}},
	}
	store.beforeList = func(store *fakeFinderFavorites) {
		if store.listCalls == 3 {
			store.nextID++
			store.items = append(store.items, finderFavoriteItem{ID: store.nextID, Name: name, Path: target})
		}
	}
	applier, _ := finderFavoriteApplier(paths, name, store)
	if err := applier.reconcileFinderFavorite(Apply); err == nil {
		t.Fatal("apply succeeded with two matching Favorites")
	}
	if store.listCalls != 3 {
		t.Fatalf("List called %d times", store.listCalls)
	}
}

func TestApplyFinderFavoriteDoesNotRemoveAnItemThatChanged(t *testing.T) {
	paths := testPaths(t)
	name := "Machine config"
	store := &fakeFinderFavorites{items: []finderFavoriteItem{
		{ID: 1, Name: name, Path: finderFavoriteTarget(paths)},
		{ID: 2, Name: name, Path: "/tmp/stale"},
	}}
	store.beforeRemove = func(store *fakeFinderFavorites, expected finderFavoriteItem) {
		for index := range store.items {
			if store.items[index].ID == expected.ID {
				store.items[index].Name = "Someone else's Favorite"
			}
		}
	}
	applier, _ := finderFavoriteApplier(paths, name, store)
	if err := applier.reconcileFinderFavorite(Apply); err == nil {
		t.Fatal("apply removed an item after its identity changed")
	}
	if len(store.items) != 2 {
		t.Fatalf("changed item was removed: %+v", store.items)
	}
}

func TestApplyFinderFavoriteRejectsASymlinkTargetBeforeWriting(t *testing.T) {
	paths := testPaths(t)
	link := filepath.Join(t.TempDir(), "repository")
	if err := os.Symlink(paths.Root, link); err != nil {
		t.Fatal(err)
	}
	paths.Root = link
	store := &fakeFinderFavorites{}
	applier, _ := finderFavoriteApplier(paths, "Machine config", store)
	err := applier.reconcileFinderFavorite(Apply)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("error = %v", err)
	}
	if len(store.operations) != 0 {
		t.Fatalf("operations = %v", store.operations)
	}
}

func finderFavoriteApplier(paths Paths, name string, store finderFavoritesStore) (Applier, *bytes.Buffer) {
	machine := testMachine()
	machine.FinderFavorite = &FinderFavorite{Name: name}
	var output bytes.Buffer
	return Applier{
		Paths:           paths,
		Machine:         machine,
		FinderFavorites: store,
		Log:             Logger{Out: &output},
	}, &output
}
