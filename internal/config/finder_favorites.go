package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

const (
	finderFavoritesID               = "finder-favorites"
	finderFavoritesName             = "Finder Favorites"
	finderFavoritesSnapshotSchema   = 1
	finderFavoriteManagedRepository = "managed-repository"
)

type finderFavoriteSnapshot struct {
	Schema    int                          `json:"schema"`
	Favorites []finderFavoriteSnapshotItem `json:"favorites"`
}

type finderFavoriteSnapshotItem struct {
	Name   string `json:"name"`
	Path   string `json:"path,omitempty"`
	Target string `json:"target,omitempty"`
}

type finderFavorite struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type finderFavoriteItem struct {
	ID   uint32
	Name string
	Path string
}

type finderFavoriteLayoutItem struct {
	Favorite finderFavorite
	Opaque   finderFavoriteItem
	Managed  bool
}

type finderFavoritesStore interface {
	List() ([]finderFavoriteItem, error)
	PutAfter(name, path string, after *finderFavoriteItem) (finderFavoriteItem, error)
	Remove(finderFavoriteItem) error
}

func finderFavoritesSnapshotPath(paths Paths) string {
	return paths.InRoot("snapshots", "finder-favorites.json")
}

func validateFinderFavoriteName(name string) error {
	if name == "" || strings.TrimSpace(name) != name {
		return fmt.Errorf("name must not be empty or have surrounding whitespace")
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return fmt.Errorf("name contains control characters")
		}
	}
	return nil
}

func (item finderFavoriteSnapshotItem) resolve(paths Paths) (finderFavorite, error) {
	if err := validateFinderFavoriteName(item.Name); err != nil {
		return finderFavorite{}, err
	}
	var target string
	switch {
	case item.Target == finderFavoriteManagedRepository && item.Path == "":
		target = paths.Root
	case item.Target == "" && item.Path != "":
		target = item.Path
		switch {
		case target == "~":
			target = paths.Home
		case strings.HasPrefix(target, "~/"):
			relative := filepath.FromSlash(strings.TrimPrefix(target, "~/"))
			if !filepath.IsLocal(relative) {
				return finderFavorite{}, fmt.Errorf("home-relative path is invalid")
			}
			target = filepath.Join(paths.Home, relative)
		}
	default:
		return finderFavorite{}, fmt.Errorf("must declare either path or target %q", finderFavoriteManagedRepository)
	}
	target = filepath.Clean(target)
	if !filepath.IsAbs(target) {
		return finderFavorite{}, fmt.Errorf("path must be absolute, ~, or begin with ~/")
	}
	return finderFavorite{Name: item.Name, Path: target}, nil
}

func snapshotFinderFavorite(paths Paths, favorite finderFavorite) finderFavoriteSnapshotItem {
	path := filepath.Clean(favorite.Path)
	if path == filepath.Clean(paths.Root) {
		return finderFavoriteSnapshotItem{Name: favorite.Name, Target: finderFavoriteManagedRepository}
	}
	if relative, err := filepath.Rel(paths.Home, path); err == nil && filepath.IsLocal(relative) {
		if relative == "." {
			path = "~"
		} else {
			path = "~/" + filepath.ToSlash(relative)
		}
	}
	return finderFavoriteSnapshotItem{Name: favorite.Name, Path: path}
}

func validateFinderFavorites(favorites []finderFavorite) error {
	seen := make(map[string]bool, len(favorites))
	for index, favorite := range favorites {
		if err := validateFinderFavoriteName(favorite.Name); err != nil {
			return fmt.Errorf("favorite %d: %w", index+1, err)
		}
		favorite.Path = filepath.Clean(favorite.Path)
		if !filepath.IsAbs(favorite.Path) {
			return fmt.Errorf("favorite %d: path is not absolute", index+1)
		}
		if seen[favorite.Path] {
			return fmt.Errorf("favorite %d repeats %s", index+1, favorite.Path)
		}
		seen[favorite.Path] = true
	}
	return nil
}

func finderFavoritesCanonical(favorites []finderFavorite) (json.RawMessage, error) {
	if favorites == nil {
		favorites = []finderFavorite{}
	}
	return json.Marshal(favorites)
}

func (b Bidirectional) finderFavoritesSaved() (json.RawMessage, []finderFavorite, []finderFavorite, bool, error) {
	data, err := os.ReadFile(finderFavoritesSnapshotPath(b.Paths))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, nil, false, err
	}
	var snapshot finderFavoriteSnapshot
	if err := decodeExactJSON(data, &snapshot); err != nil {
		return nil, nil, nil, true, err
	}
	if snapshot.Schema != finderFavoritesSnapshotSchema || snapshot.Favorites == nil {
		return nil, nil, nil, true, fmt.Errorf("unsupported Finder Favorites snapshot")
	}
	favorites := make([]finderFavorite, 0, len(snapshot.Favorites))
	for index, item := range snapshot.Favorites {
		favorite, resolveErr := item.resolve(b.Paths)
		if resolveErr != nil {
			return nil, nil, nil, true, fmt.Errorf("favorite %d: %w", index+1, resolveErr)
		}
		favorites = append(favorites, favorite)
	}
	if err := validateFinderFavorites(favorites); err != nil {
		return nil, nil, nil, true, err
	}
	var missing []finderFavorite
	for _, favorite := range favorites {
		if info, statErr := os.Stat(favorite.Path); statErr != nil || !info.IsDir() {
			missing = append(missing, favorite)
		}
	}
	canonical, err := finderFavoritesCanonical(favorites)
	return canonical, favorites, missing, true, err
}

func directoryFinderFavorites(items []finderFavoriteItem) ([]finderFavorite, []finderFavoriteItem, error) {
	var favorites []finderFavorite
	var opaque []finderFavoriteItem
	for _, item := range items {
		favorite, managed := directoryFinderFavorite(item)
		if !managed {
			opaque = append(opaque, item)
			continue
		}
		favorites = append(favorites, favorite)
	}
	if err := validateFinderFavorites(favorites); err != nil {
		return nil, nil, err
	}
	return favorites, opaque, nil
}

func directoryFinderFavorite(item finderFavoriteItem) (finderFavorite, bool) {
	if item.Path == "" {
		return finderFavorite{}, false
	}
	path := filepath.Clean(item.Path)
	if !filepath.IsAbs(path) {
		return finderFavorite{}, false
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return finderFavorite{}, false
	}
	return finderFavorite{Name: item.Name, Path: path}, true
}

func finderFavoritesLive(store finderFavoritesStore) (json.RawMessage, []finderFavorite, []finderFavoriteItem, error) {
	items, err := store.List()
	if err != nil {
		return nil, nil, nil, err
	}
	favorites, opaque, err := directoryFinderFavorites(items)
	if err != nil {
		return nil, nil, nil, err
	}
	canonical, err := finderFavoritesCanonical(favorites)
	return canonical, favorites, opaque, err
}

func finderFavoritesLayout(items []finderFavoriteItem) ([]finderFavoriteLayoutItem, error) {
	layout := make([]finderFavoriteLayoutItem, 0, len(items))
	var managed []finderFavorite
	for _, item := range items {
		favorite, ok := directoryFinderFavorite(item)
		if ok {
			managed = append(managed, favorite)
			layout = append(layout, finderFavoriteLayoutItem{Favorite: favorite, Managed: true})
		} else {
			layout = append(layout, finderFavoriteLayoutItem{Opaque: item})
		}
	}
	if err := validateFinderFavorites(managed); err != nil {
		return nil, err
	}
	return layout, nil
}

func desiredFinderFavoritesLayout(items []finderFavoriteItem, desired []finderFavorite) ([]finderFavoriteLayoutItem, error) {
	original, err := finderFavoritesLayout(items)
	if err != nil {
		return nil, err
	}
	layout := make([]finderFavoriteLayoutItem, 0, max(len(original), len(desired)))
	index := 0
	for _, item := range original {
		if !item.Managed {
			layout = append(layout, item)
			continue
		}
		if index < len(desired) {
			layout = append(layout, finderFavoriteLayoutItem{Favorite: desired[index], Managed: true})
			index++
		}
	}
	for index < len(desired) {
		layout = append(layout, finderFavoriteLayoutItem{Favorite: desired[index], Managed: true})
		index++
	}
	return layout, nil
}

func finderFavoritesDiff(saved, live []finderFavorite) []string {
	var details []string
	for _, favorite := range live {
		if !slices.Contains(saved, favorite) {
			details = append(details, "Only on this Mac: "+favorite.Name)
		}
	}
	for _, favorite := range saved {
		if !slices.Contains(live, favorite) {
			details = append(details, "Only in the saved Favorites: "+favorite.Name)
		}
	}
	if len(details) == 0 && !slices.Equal(saved, live) {
		details = append(details, "The same Favorites are in a different order")
	}
	return details
}

var finderFavoritesWords = bidirectionalWords{
	saved:   "the saved Favorites",
	live:    "Finder Favorites on this Mac",
	capture: "Save this Mac's Favorites",
	restore: "Restore the saved Favorites",
}

func (b Bidirectional) InspectFinderFavorites(store finderFavoritesStore) Resource {
	resource := Resource{ID: finderFavoritesID, Name: finderFavoritesName, Bidirectional: true}
	saved, favorites, missing, hasSaved, savedErr := b.finderFavoritesSaved()
	live, liveFavorites, _, liveErr := finderFavoritesLive(store)
	if savedErr != nil || liveErr != nil {
		resource.State = Unavailable
		resource.Summary = "Finder Favorites unavailable"
		if savedErr != nil {
			resource.Checks = append(resource.Checks, no("saved Finder Favorites valid", savedErr.Error()))
		}
		if liveErr != nil {
			resource.Checks = append(resource.Checks, no("Finder Favorites readable", liveErr.Error()))
		}
		return resource
	}
	if !hasSaved {
		resource.State = Uncaptured
		resource.Summary = "Finder Favorites are not captured"
		resource.Actions = []Action{Capture}
		resource.ActionLabels = map[Action]string{Capture: "Capture this Mac's Favorites"}
		return resource
	}
	baseline, hasBaseline, _ := b.Baselines.Load(resource.ID)
	finderFavoritesWords.offer(&resource, Classify(saved, live, baseline, hasBaseline))
	resource.Details = finderFavoritesDiff(favorites, liveFavorites)
	if len(missing) > 0 {
		resource.Checks = append(resource.Checks, Check{Label: FormatCount(len(missing), "saved Favorite unavailable", "saved Favorites unavailable")})
		for _, favorite := range missing {
			resource.Details = append(resource.Details, "Unavailable: "+favorite.Name)
		}
		if resource.State != Current {
			resource.Actions = []Action{Capture}
			resource.Summary += " · " + FormatCount(len(missing), "saved target unavailable", "saved targets unavailable")
		}
	}
	return resource
}

func (b Bidirectional) CaptureFinderFavorites(store finderFavoritesStore) error {
	_, favorites, _, err := finderFavoritesLive(store)
	if err != nil {
		return fmt.Errorf("read Finder Favorites: %w", err)
	}
	items := make([]finderFavoriteSnapshotItem, 0, len(favorites))
	for _, favorite := range favorites {
		items = append(items, snapshotFinderFavorite(b.Paths, favorite))
	}
	snapshot := finderFavoriteSnapshot{Schema: finderFavoritesSnapshotSchema, Favorites: items}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Finder Favorites: %w", err)
	}
	return atomicWrite(finderFavoritesSnapshotPath(b.Paths), append(data, '\n'), 0o644)
}

func (b Bidirectional) MarkFinderFavoritesIfCurrent(store finderFavoritesStore) error {
	saved, _, _, hasSaved, err := b.finderFavoritesSaved()
	if err != nil {
		return err
	}
	if !hasSaved {
		return nil
	}
	live, _, _, err := finderFavoritesLive(store)
	if err != nil {
		return err
	}
	if !bytes.Equal(saved, live) {
		return fmt.Errorf("Finder Favorites are not synchronized")
	}
	return b.Baselines.Save(finderFavoritesID, saved)
}

func replaceFinderFavorites(store finderFavoritesStore, target []finderFavoriteLayoutItem) error {
	wanted := make(map[string]bool)
	var after *finderFavoriteItem
	for _, item := range target {
		if !item.Managed {
			current := item.Opaque
			after = &current
			continue
		}
		inserted, err := store.PutAfter(item.Favorite.Name, item.Favorite.Path, after)
		if err != nil {
			return err
		}
		wanted[item.Favorite.Path] = true
		after = &inserted
	}
	items, err := store.List()
	if err != nil {
		return err
	}
	for _, item := range items {
		favorite, managed := directoryFinderFavorite(item)
		if !managed || wanted[favorite.Path] {
			continue
		}
		if err := store.Remove(item); err != nil {
			return err
		}
	}
	items, err = store.List()
	if err != nil {
		return err
	}
	actual, err := finderFavoritesLayout(items)
	if err != nil {
		return err
	}
	if !slices.Equal(actual, target) {
		return fmt.Errorf("Finder Favorites did not match the requested layout")
	}
	return nil
}

func (e Applier) reconcileFinderFavorites(action Action) error {
	switch action {
	case Capture:
		if err := e.Bidir.CaptureFinderFavorites(e.FinderFavorites); err != nil {
			return err
		}
		e.Log.OK("live Favorites captured")
		return nil
	case Apply:
		return e.applyFinderFavorites()
	default:
		return nil
	}
}

func (e Applier) applyFinderFavorites() error {
	_, desired, missing, hasSaved, err := e.Bidir.finderFavoritesSaved()
	if err != nil {
		return err
	}
	if !hasSaved {
		return advisoryError{"no saved Finder Favorites; Finder left untouched"}
	}
	if len(missing) > 0 {
		return advisoryError{FormatCount(len(missing), "saved Favorite is unavailable", "saved Favorites are unavailable") + "; Finder left untouched"}
	}
	originalItems, err := e.FinderFavorites.List()
	if err != nil {
		return fmt.Errorf("read Finder Favorites: %w", err)
	}
	original, _, err := directoryFinderFavorites(originalItems)
	if err != nil {
		return err
	}
	originalLayout, err := finderFavoritesLayout(originalItems)
	if err != nil {
		return err
	}
	if slices.Equal(original, desired) {
		e.Log.OK("Favorites already current")
		return nil
	}
	desiredLayout, err := desiredFinderFavoritesLayout(originalItems, desired)
	if err != nil {
		return err
	}
	if err := replaceFinderFavorites(e.FinderFavorites, desiredLayout); err != nil {
		return restoreFinderFavoritesAfterFailure(e.FinderFavorites, originalLayout, err)
	}
	finalItems, err := e.FinderFavorites.List()
	if err != nil {
		return restoreFinderFavoritesAfterFailure(e.FinderFavorites, originalLayout, err)
	}
	finalLayout, err := finderFavoritesLayout(finalItems)
	if err != nil {
		return restoreFinderFavoritesAfterFailure(e.FinderFavorites, originalLayout, fmt.Errorf("verify applied Finder Favorites: %w", err))
	}
	if !slices.Equal(finalLayout, desiredLayout) {
		return restoreFinderFavoritesAfterFailure(e.FinderFavorites, originalLayout, errors.New("applied Favorites failed final verification"))
	}
	e.Log.OK("saved Favorites restored")
	return nil
}

func restoreFinderFavoritesAfterFailure(store finderFavoritesStore, original []finderFavoriteLayoutItem, failure error) error {
	if err := replaceFinderFavorites(store, original); err != nil {
		return errors.Join(failure, fmt.Errorf("restore original Finder Favorites: %w", err))
	}
	return fmt.Errorf("Finder Favorites change failed; original list restored: %w", failure)
}
