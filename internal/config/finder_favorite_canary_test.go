//go:build darwin && config_native_canary

package config

import "testing"

func TestFinderFavoritesNativeCanary(t *testing.T) {
	if _, err := (darwinFinderFavorites{}).List(); err != nil {
		t.Fatalf("list Finder Favorites through SharedFileList: %v", err)
	}
}
