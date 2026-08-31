package config

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"howett.net/plist"
)

const (
	dockDomain = "com.apple.dock"
	dockKey    = "persistent-apps"
)

type dockState struct {
	Present bool
	Tiles   []any
}

type dockStore interface {
	Read() (dockState, error)
	Write(dockState) error
}

type defaultsDockStore struct {
	Runner Runner
	Live   LiveRunner
}

func (s defaultsDockStore) Read() (dockState, error) {
	result := run(s.Runner, "defaults", "export", dockDomain, "-")
	if result.Err != nil {
		return dockState{}, result.Failure()
	}
	values, err := decodePlist([]byte(result.Stdout))
	if err != nil {
		return dockState{}, err
	}
	raw, present := values[dockKey]
	if !present {
		return dockState{}, nil
	}
	tiles, ok := raw.([]any)
	if !ok {
		return dockState{}, fmt.Errorf("%s is not an array", dockKey)
	}
	return dockState{Present: true, Tiles: tiles}, nil
}

func (s defaultsDockStore) Write(state dockState) error {
	if !state.Present {
		return s.Live.Command("defaults", "delete", dockDomain, dockKey)
	}
	// defaults parses an XML property list for the value argument and preserves
	// every scalar type. OpenStep has no integer, boolean, real, or date type, so
	// encoding tiles that way rewrites GUID, file-type, and dock-extra as strings.
	value, err := plist.Marshal(state.Tiles, plist.XMLFormat)
	if err != nil {
		return fmt.Errorf("encode %s: %w", dockKey, err)
	}
	if err := s.Live.Command("defaults", "write", dockDomain, dockKey, string(value)); err != nil {
		for unwrapped := errors.Unwrap(err); unwrapped != nil; unwrapped = errors.Unwrap(err) {
			err = unwrapped
		}
		return fmt.Errorf("defaults write %s %s: %w", dockDomain, dockKey, err)
	}
	return nil
}

func dockSnapshotPath(paths Paths) string {
	return paths.InRoot("snapshots", "dock.apps")
}

func (b Bidirectional) dockSaved() (json.RawMessage, []string, []string, bool, error) {
	data, err := os.ReadFile(dockSnapshotPath(b.Paths))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, nil, false, err
	}
	var all, present []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "~/") {
			line = filepath.Join(b.Paths.Home, strings.TrimPrefix(line, "~/"))
		}
		line = filepath.Clean(line)
		all = append(all, line)
		if info, statErr := os.Stat(line); statErr == nil && info.IsDir() {
			present = append(present, line)
		}
	}
	// Canonicalize what the snapshot records, not what survives on disk. The
	// live side is every decodable tile, so filtering this one by existence
	// would compare two different reductions and leave a resource whose saved
	// app was deleted permanently unable to converge.
	canonical, err := json.Marshal(all)
	return canonical, all, present, true, err
}

func dockAppPath(tile any) (string, bool) {
	entry, ok := tile.(map[string]any)
	if !ok || entry["tile-type"] != "file-tile" {
		return "", false
	}
	tileData, ok := entry["tile-data"].(map[string]any)
	if !ok {
		return "", false
	}
	fileData, ok := tileData["file-data"].(map[string]any)
	if !ok {
		return "", false
	}
	location, ok := fileData["_CFURLString"].(string)
	if !ok {
		return "", false
	}
	parsed, err := url.Parse(location)
	if err != nil {
		return "", false
	}
	var path string
	switch parsed.Scheme {
	case "":
		path = location
	case "file":
		if parsed.Host != "" && parsed.Host != "localhost" {
			return "", false
		}
		path = parsed.Path
	default:
		return "", false
	}
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) || !strings.EqualFold(filepath.Ext(path), ".app") {
		return "", false
	}
	return path, true
}

func dockAppPaths(state dockState) []string {
	var paths []string
	for _, tile := range state.Tiles {
		if path, ok := dockAppPath(tile); ok {
			paths = append(paths, path)
		}
	}
	return paths
}

func dockOpaqueTiles(state dockState) []any {
	var tiles []any
	for _, tile := range state.Tiles {
		if _, ok := dockAppPath(tile); !ok {
			tiles = append(tiles, tile)
		}
	}
	return tiles
}

func dockGUID(tile any) (uint64, bool) {
	entry, ok := tile.(map[string]any)
	if !ok {
		return 0, false
	}
	switch value := entry["GUID"].(type) {
	case uint64:
		return value, true
	case int64:
		return uint64(value), value >= 0
	case int:
		return uint64(value), value >= 0
	default:
		return 0, false
	}
}

func newDockGUID(used map[uint64]bool) (uint64, error) {
	// Dock addresses tiles by GUID. Keep generated identifiers in the range
	// used by native Dock tooling and refuse every identifier already present.
	const (
		first         = uint64(1_000_000_000)
		lastExclusive = uint64(9_999_999_999)
	)
	space := new(big.Int).SetUint64(lastExclusive - first)
	for {
		random, err := rand.Int(rand.Reader, space)
		if err != nil {
			return 0, err
		}
		guid := random.Uint64() + first
		if !used[guid] {
			used[guid] = true
			return guid, nil
		}
	}
}

func newDockAppTile(path string, guid uint64) map[string]any {
	location := (&url.URL{Scheme: "file", Path: filepath.Clean(path) + string(filepath.Separator)}).String()
	return map[string]any{
		"GUID": guid,
		"tile-data": map[string]any{
			"file-data": map[string]any{
				"_CFURLString":     location,
				"_CFURLStringType": uint64(15),
			},
			"file-label": strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
			"file-type":  uint64(41),
		},
		"tile-type": "file-tile",
	}
}

// reconcileDockTiles treats every decodable application tile as the captured
// layout and every other tile as opaque Dock-owned state. Existing application
// dictionaries are reused by path so bookmarks and metadata survive a reorder.
func reconcileDockTiles(original dockState, desired []string) (dockState, error) {
	byPath := make(map[string][]any)
	usedGUIDs := make(map[uint64]bool)
	lastApp := -1
	for index, tile := range original.Tiles {
		if guid, ok := dockGUID(tile); ok {
			usedGUIDs[guid] = true
		}
		if path, ok := dockAppPath(tile); ok {
			byPath[path] = append(byPath[path], tile)
			lastApp = index
		}
	}

	apps := make([]any, 0, len(desired))
	for _, path := range desired {
		matches := byPath[path]
		if len(matches) > 0 {
			apps = append(apps, matches[0])
			byPath[path] = matches[1:]
			continue
		}
		guid, err := newDockGUID(usedGUIDs)
		if err != nil {
			return dockState{}, fmt.Errorf("create Dock tile identity: %w", err)
		}
		apps = append(apps, newDockAppTile(path, guid))
	}

	tiles := make([]any, 0, len(original.Tiles)+max(0, len(apps)-len(dockAppPaths(original))))
	next := 0
	for index, tile := range original.Tiles {
		if _, ok := dockAppPath(tile); ok {
			if next < len(apps) {
				tiles = append(tiles, apps[next])
				next++
			}
			if index == lastApp {
				tiles = append(tiles, apps[next:]...)
				next = len(apps)
			}
			continue
		}
		tiles = append(tiles, tile)
	}
	if lastApp < 0 {
		tiles = append(tiles, apps...)
	}
	return dockState{Present: true, Tiles: tiles}, nil
}

func (b Bidirectional) dockStore() dockStore {
	if b.Dock != nil {
		return b.Dock
	}
	return defaultsDockStore{Runner: b.Runner, Live: NewMachineLiveRunner(b.Paths)}
}

func (b Bidirectional) dockLive() (json.RawMessage, []string, error) {
	state, err := b.dockStore().Read()
	if err != nil {
		return nil, nil, err
	}
	apps := dockAppPaths(state)
	canonical, err := json.Marshal(apps)
	return canonical, apps, err
}

func dockDiff(saved, live []string) []string {
	var details []string
	for _, app := range live {
		if !slices.Contains(saved, app) {
			details = append(details, "Only on this Mac: "+filepath.Base(app))
		}
	}
	for _, app := range saved {
		if !slices.Contains(live, app) {
			details = append(details, "Only in the saved layout: "+filepath.Base(app))
		}
	}
	if len(details) == 0 && !slices.Equal(saved, live) {
		details = append(details, "The same apps are in a different order")
	}
	return details
}

const (
	dockID   = "dock"
	dockName = "Dock"
)

var dockWords = bidirectionalWords{
	saved:   "the saved layout",
	live:    "the Dock on this Mac",
	capture: "Save this Mac's layout",
	restore: "Restore the saved layout",
}

func (b Bidirectional) InspectDock() Resource {
	resource := Resource{ID: dockID, Name: dockName, Bidirectional: true}
	saved, all, present, hasSaved, savedErr := b.dockSaved()
	live, liveApps, liveErr := b.dockLive()
	if savedErr != nil || liveErr != nil {
		resource.State = Unavailable
		resource.Summary = "Dock state unavailable"
		if savedErr != nil {
			resource.Checks = append(resource.Checks, no("saved Dock layout valid", savedErr.Error()))
		}
		if liveErr != nil {
			resource.Checks = append(resource.Checks, no("Dock layout readable", liveErr.Error()))
		}
		return resource
	}
	if !hasSaved {
		resource.State = Uncaptured
		resource.Summary = "the Dock layout is not captured"
		resource.Actions = []Action{Capture}
		resource.ActionLabels = map[Action]string{Capture: "Capture this Mac's layout"}
		return resource
	}
	missing := len(all) - len(present)
	baseline, hasBaseline, _ := b.Baselines.Load(resource.ID)
	dockWords.offer(&resource, Classify(saved, live, baseline, hasBaseline))
	resource.Details = dockDiff(present, liveApps)
	if missing > 0 {
		resource.Checks = append(resource.Checks, Check{
			Label: FormatCount(missing, "saved Dock app unavailable", "saved Dock apps unavailable"),
		})
		for _, app := range all {
			if !slices.Contains(present, app) {
				resource.Details = append(resource.Details, "Unavailable: "+filepath.Base(app))
			}
		}
	}
	if missing > 0 && resource.State != Current {
		resource.Actions = []Action{Capture}
		resource.Summary += " · " + FormatCount(missing, "saved app unavailable", "saved apps unavailable")
	}
	return resource
}

func (b Bidirectional) CaptureDock() error {
	_, live, err := b.dockLive()
	if err != nil {
		return fmt.Errorf("read Dock layout: %w", err)
	}
	var lines []string
	for _, app := range live {
		if strings.HasPrefix(app, b.Paths.Home+string(filepath.Separator)) {
			app = "~/" + strings.TrimPrefix(app, b.Paths.Home+string(filepath.Separator))
		}
		lines = append(lines, app)
	}
	return atomicWrite(dockSnapshotPath(b.Paths), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func (b Bidirectional) MarkDockIfCurrent() error {
	saved, _, _, hasSaved, savedErr := b.dockSaved()
	live, _, liveErr := b.dockLive()
	if savedErr != nil || liveErr != nil || !hasSaved || string(saved) != string(live) {
		return fmt.Errorf("Dock is not synchronized")
	}
	return b.Baselines.Save(dockID, saved)
}

func restoreDockAfterFailure(store dockStore, original dockState, failure error) error {
	if err := store.Write(original); err != nil {
		return errors.Join(failure, fmt.Errorf("restore original Dock layout: %w", err))
	}
	restored, err := store.Read()
	if err != nil {
		return errors.Join(failure, fmt.Errorf("verify original Dock layout: %w", err))
	}
	if !reflect.DeepEqual(restored, original) {
		return errors.Join(failure, fmt.Errorf("original Dock layout did not restore exactly"))
	}
	return fmt.Errorf("Dock change failed; original layout restored: %w", failure)
}
