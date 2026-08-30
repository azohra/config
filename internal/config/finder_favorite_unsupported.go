//go:build !darwin

package config

import "fmt"

type unsupportedFinderFavorites struct{}

func newFinderFavoritesStore() finderFavoritesStore { return unsupportedFinderFavorites{} }

func (unsupportedFinderFavorites) List() ([]finderFavoriteItem, error) {
	return nil, fmt.Errorf("Finder Favorites require macOS")
}

func (unsupportedFinderFavorites) Add(string, string) error {
	return fmt.Errorf("Finder Favorites require macOS")
}

func (unsupportedFinderFavorites) Remove(finderFavoriteItem) error {
	return fmt.Errorf("Finder Favorites require macOS")
}
