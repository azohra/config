//go:build darwin && config_native_canary

package config

import (
	"path/filepath"
	"testing"
)

// TestFinderFavoritesNativeCanary reads the real macOS shared-file-list API.
// Asserting only that the call returned would pass on a list with nothing in
// it, which proves dlopen and dlsym and nothing else. A Mac always has
// Favorites, so the list has to carry usable entries and read the same way
// twice.
func TestFinderFavoritesNativeCanary(t *testing.T) {
	store := darwinFinderFavorites{}
	items, err := store.List()
	if err != nil {
		t.Fatalf("list Finder Favorites through SharedFileList: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("the sidebar reported no Favorites at all")
	}
	paths := 0
	for _, item := range items {
		if item.Path == "" {
			continue
		}
		if !filepath.IsAbs(item.Path) {
			t.Errorf("Favorite %q resolved to a relative path %q", item.Name, item.Path)
		}
		paths++
	}
	if paths == 0 {
		t.Fatal("no Favorite resolved to a path, so nothing was decoded")
	}

	again, err := store.List()
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if len(again) != len(items) {
		t.Fatalf("two reads disagree: %d then %d Favorites", len(items), len(again))
	}
	for index := range items {
		if again[index].Path != items[index].Path || again[index].Name != items[index].Name {
			t.Fatalf("entry %d changed between reads: %+v then %+v", index, items[index], again[index])
		}
	}
}
