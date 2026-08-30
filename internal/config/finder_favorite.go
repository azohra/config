package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	finderFavoriteID   = "finder-favorite"
	finderFavoriteName = "Finder Favorite"
)

type FinderFavorite struct {
	Name string `toml:"name"`
}

func (f FinderFavorite) Validate() error {
	if f.Name == "" || strings.TrimSpace(f.Name) != f.Name {
		return fmt.Errorf("name must not be empty or have surrounding whitespace")
	}
	for _, character := range f.Name {
		if unicode.IsControl(character) {
			return fmt.Errorf("name contains control characters")
		}
	}
	return nil
}

type finderFavoriteItem struct {
	ID   uint32
	Name string
	Path string
}

type finderFavoritesStore interface {
	List() ([]finderFavoriteItem, error)
	Add(name, path string) error
	Remove(finderFavoriteItem) error
}

func finderFavoriteTarget(paths Paths) string {
	return filepath.Clean(paths.Root)
}

func validateFinderFavoriteTarget(paths Paths) (string, error) {
	target := finderFavoriteTarget(paths)
	if !filepath.IsAbs(target) {
		return "", fmt.Errorf("managed repository path is not absolute")
	}
	info, err := os.Lstat(target)
	if err != nil {
		return "", fmt.Errorf("inspect managed repository: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("managed repository is not a directory")
	}
	return target, nil
}

func matchingFinderFavorites(items []finderFavoriteItem, name string) []finderFavoriteItem {
	var matching []finderFavoriteItem
	for _, item := range items {
		if item.Name == name {
			matching = append(matching, item)
		}
	}
	return matching
}

func finderFavoriteCurrent(items []finderFavoriteItem, name, target string) bool {
	matching := matchingFinderFavorites(items, name)
	return len(matching) == 1 && filepath.Clean(matching[0].Path) == target
}

func InspectFinderFavorite(paths Paths, declaration FinderFavorite, store finderFavoritesStore) Resource {
	resource := Resource{ID: finderFavoriteID, Name: finderFavoriteName, Authoritative: true}
	target, err := validateFinderFavoriteTarget(paths)
	if err != nil {
		resource.State = Unavailable
		resource.Summary = "The managed repository is unavailable"
		resource.Checks = []Check{no("Managed repository available", err.Error())}
		return resource
	}
	items, err := store.List()
	if err != nil {
		resource.State = Unavailable
		resource.Summary = "Finder Favorites are unreadable"
		resource.Checks = []Check{no("Finder Favorites readable", err.Error())}
		return resource
	}
	matching := matchingFinderFavorites(items, declaration.Name)
	if finderFavoriteCurrent(items, declaration.Name, target) {
		resource.State = Current
		resource.Summary = declaration.Name + " opens the managed repository"
		resource.Checks = []Check{yes("Finder Favorite current")}
		return resource
	}
	resource.State = Drift
	resource.Checks = []Check{no("Finder Favorite current", declaration.Name+" must open Config's managed repository")}
	resource.Actions = []Action{Apply}
	resource.ActionLabels = map[Action]string{Apply: "Add the Finder Favorite"}
	if len(matching) == 0 {
		resource.Summary = declaration.Name + " is not in Finder"
	} else {
		resource.Summary = declaration.Name + " does not identify the managed repository"
	}
	return resource
}

func (e Applier) reconcileFinderFavorite(action Action) error {
	if action != Apply || e.Machine.FinderFavorite == nil {
		return nil
	}
	target, err := validateFinderFavoriteTarget(e.Paths)
	if err != nil {
		return err
	}
	store := e.FinderFavorites
	if store == nil {
		store = newFinderFavoritesStore()
	}
	name := e.Machine.FinderFavorite.Name
	items, err := store.List()
	if err != nil {
		return fmt.Errorf("read Finder Favorites: %w", err)
	}
	if finderFavoriteCurrent(items, name, target) {
		e.Log.OK("Finder Favorite already current")
		return nil
	}

	matching := matchingFinderFavorites(items, name)
	keeper, hasKeeper := desiredFinderFavorite(matching, target)
	if !hasKeeper {
		if err := store.Add(name, target); err != nil {
			return fmt.Errorf("add Finder Favorite: %w", err)
		}
		items, err = store.List()
		if err != nil {
			return fmt.Errorf("verify added Finder Favorite: %w", err)
		}
		matching = matchingFinderFavorites(items, name)
		keeper, hasKeeper = desiredFinderFavorite(matching, target)
		if !hasKeeper {
			return fmt.Errorf("Finder did not add %q for the managed repository", name)
		}
	}

	for _, item := range matching {
		if item.ID == keeper.ID {
			continue
		}
		if err := store.Remove(item); err != nil {
			return fmt.Errorf("remove stale Finder Favorite %q: %w", name, err)
		}
	}
	items, err = store.List()
	if err != nil {
		return fmt.Errorf("verify Finder Favorite: %w", err)
	}
	if !finderFavoriteCurrent(items, name, target) {
		return fmt.Errorf("Finder Favorite %q did not converge to the managed repository", name)
	}
	e.Log.OK("Finder Favorite added")
	return nil
}

func desiredFinderFavorite(items []finderFavoriteItem, target string) (finderFavoriteItem, bool) {
	for _, item := range items {
		if filepath.Clean(item.Path) == target {
			return item, true
		}
	}
	return finderFavoriteItem{}, false
}
