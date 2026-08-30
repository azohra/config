package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"howett.net/plist"
)

const (
	chromePWAsID     = "chrome-pwas"
	chromePWAsName   = "Chrome PWAs"
	chromePWAsSchema = 1
	chromeBundleID   = "com.google.Chrome"
)

var chromePWAWords = bidirectionalWords{
	saved:   "the saved PWAs",
	live:    "PWAs on this Mac",
	capture: "Back up this Mac's PWAs",
	restore: "Restore the saved PWAs",
}

var (
	chromePWAIDPattern     = regexp.MustCompile(`^[a-p]{32}$`)
	chromePWASchemePattern = regexp.MustCompile(`^[a-z][a-z0-9+.-]*$`)
)

type chromePWASnapshot struct {
	Schema int         `json:"schema"`
	Apps   []chromePWA `json:"apps"`
}

type chromePWA struct {
	Name       string   `json:"name"`
	ID         string   `json:"id"`
	URL        string   `json:"url"`
	Schemes    []string `json:"schemes,omitempty"`
	IconSHA256 string   `json:"icon_sha256"`
}

type liveChromePWA struct {
	chromePWA
	Path     string
	IconPath string
}

func chromePWASnapshotPath(paths Paths) string {
	return paths.InRoot("snapshots", "chrome-pwas.json")
}

func chromePWAIconDir(paths Paths) string {
	return paths.InRoot("snapshots", "chrome-pwas")
}

func chromePWAIconPath(paths Paths, id string) string {
	return filepath.Join(chromePWAIconDir(paths), id+".icns")
}

func chromePWALiveDir(paths Paths) string {
	return paths.InHome("Applications", "Chrome Apps.localized")
}

func chromePWALoaderPath() string {
	return "/Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Framework.framework/Versions/Current/Helpers/app_mode_loader"
}

func chromePWATemplatePath() string {
	return "/Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Framework.framework/Versions/Current/Resources/app_mode-Info.plist"
}

func validateChromePWA(app chromePWA) error {
	if app.Name == "" || app.Name == "." || app.Name == ".." || strings.ContainsAny(app.Name, `/\\`) {
		return fmt.Errorf("invalid PWA name %q", app.Name)
	}
	for _, character := range app.Name {
		if unicode.IsControl(character) {
			return fmt.Errorf("invalid PWA name %q", app.Name)
		}
	}
	if !chromePWAIDPattern.MatchString(app.ID) {
		return fmt.Errorf("invalid PWA ID %q", app.ID)
	}
	parsed, err := url.Parse(app.URL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return fmt.Errorf("invalid URL for %s", app.Name)
	}
	if len(app.IconSHA256) != sha256.Size*2 {
		return fmt.Errorf("invalid icon digest for %s", app.Name)
	}
	if _, err := hex.DecodeString(app.IconSHA256); err != nil {
		return fmt.Errorf("invalid icon digest for %s", app.Name)
	}
	for _, scheme := range app.Schemes {
		if !chromePWASchemePattern.MatchString(scheme) {
			return fmt.Errorf("invalid URL scheme %q for %s", scheme, app.Name)
		}
	}
	return nil
}

func normalizeChromePWAs(apps []chromePWA) ([]chromePWA, error) {
	normalized := make([]chromePWA, len(apps))
	copy(normalized, apps)
	seenIDs := make(map[string]bool, len(normalized))
	seenNames := make(map[string]bool, len(normalized))
	for index := range normalized {
		normalized[index].Schemes = slices.Clone(normalized[index].Schemes)
		slices.Sort(normalized[index].Schemes)
		normalized[index].Schemes = slices.Compact(normalized[index].Schemes)
		if err := validateChromePWA(normalized[index]); err != nil {
			return nil, err
		}
		name := strings.ToLower(normalized[index].Name)
		if seenIDs[normalized[index].ID] || seenNames[name] {
			return nil, fmt.Errorf("duplicate PWA %s", normalized[index].Name)
		}
		seenIDs[normalized[index].ID] = true
		seenNames[name] = true
	}
	slices.SortFunc(normalized, func(left, right chromePWA) int { return strings.Compare(left.ID, right.ID) })
	return normalized, nil
}

// canonicalChromePWAs reduces a collection to the fact Config tracks: which
// PWAs are installed. Chrome owns everything inside a bundle and rewrites it
// whenever a site changes its icon or manifest, so comparing that content
// would report Chrome's churn as a choice for the reader to make. The rest of
// the record is kept because a restore has to rebuild the bundle from it.
func canonicalChromePWAs(apps []chromePWA) (json.RawMessage, []chromePWA, error) {
	normalized, err := normalizeChromePWAs(apps)
	if err != nil {
		return nil, nil, err
	}
	installed := make([]string, len(normalized))
	for index, app := range normalized {
		installed[index] = app.ID
	}
	canonical, err := json.Marshal(installed)
	return canonical, normalized, err
}

// chromePWACollectionsEqual compares the full records, which is what a
// restore needs: identity alone cannot say whether a bundle must be rebuilt.
func chromePWACollectionsEqual(saved []chromePWA, live []liveChromePWA) bool {
	if len(saved) != len(live) {
		return false
	}
	for index := range saved {
		if !chromePWAEqual(saved[index], live[index].chromePWA) {
			return false
		}
	}
	return true
}

func chromePWAEqual(left, right chromePWA) bool {
	return left.Name == right.Name && left.ID == right.ID && left.URL == right.URL &&
		left.IconSHA256 == right.IconSHA256 && slices.Equal(left.Schemes, right.Schemes)
}

func iconDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// chromePWAIdentity answers whether a bundle is one of Chrome's PWAs, from
// its plist alone. The icon completes the record but cannot identify it, so
// a bundle Config does not manage is never opened for one.
func chromePWAIdentity(data []byte) (chromePWA, bool, error) {
	values, err := decodePlist(data)
	if err != nil {
		return chromePWA{}, false, err
	}
	id, _ := values["CrAppModeShortcutID"].(string)
	if id == "" {
		return chromePWA{}, false, nil
	}
	name, _ := values["CrAppModeShortcutName"].(string)
	shortcutURL, _ := values["CrAppModeShortcutURL"].(string)
	var schemes []string
	if urlTypes, ok := values["CFBundleURLTypes"].([]any); ok {
		for _, rawType := range urlTypes {
			urlType, ok := rawType.(map[string]any)
			if !ok {
				continue
			}
			if rawSchemes, ok := urlType["CFBundleURLSchemes"].([]any); ok {
				for _, rawScheme := range rawSchemes {
					if scheme, ok := rawScheme.(string); ok {
						schemes = append(schemes, scheme)
					}
				}
			}
		}
	}
	return chromePWA{Name: name, ID: id, URL: shortcutURL, Schemes: schemes}, true, nil
}

func (b Bidirectional) chromePWASaved() (json.RawMessage, []chromePWA, bool, error) {
	data, err := os.ReadFile(chromePWASnapshotPath(b.Paths))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	var snapshot chromePWASnapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, nil, true, err
	}
	if snapshot.Schema != chromePWAsSchema || snapshot.Apps == nil {
		return nil, nil, true, fmt.Errorf("unsupported PWA snapshot")
	}
	canonical, apps, err := canonicalChromePWAs(snapshot.Apps)
	if err != nil {
		return nil, nil, true, err
	}
	for _, app := range apps {
		icon, readErr := os.ReadFile(chromePWAIconPath(b.Paths, app.ID))
		if readErr != nil {
			return nil, nil, true, fmt.Errorf("read icon for %s: %w", app.Name, readErr)
		}
		if iconDigest(icon) != app.IconSHA256 {
			return nil, nil, true, fmt.Errorf("saved icon for %s does not match its manifest", app.Name)
		}
	}
	return canonical, apps, true, nil
}

// chromePWALive reads the installed collection. Anything Config cannot
// identify as a Chrome PWA is not Config's to manage and is passed over; a
// PWA Config can identify but not read is named, so one damaged bundle
// reports itself instead of hiding every other app.
func (b Bidirectional) chromePWALive() (json.RawMessage, []liveChromePWA, []string, error) {
	entries, err := os.ReadDir(chromePWALiveDir(b.Paths))
	if errors.Is(err, os.ErrNotExist) {
		canonical, _, canonicalErr := canonicalChromePWAs(nil)
		return canonical, nil, nil, canonicalErr
	}
	if err != nil {
		return nil, nil, nil, err
	}
	var live []liveChromePWA
	var damaged []string
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".app") {
			continue
		}
		bundle := filepath.Join(chromePWALiveDir(b.Paths), entry.Name())
		info, readErr := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist"))
		if readErr != nil {
			continue
		}
		app, isPWA, parseErr := chromePWAIdentity(info)
		if parseErr != nil || !isPWA {
			continue
		}
		iconPath := filepath.Join(bundle, "Contents", "Resources", "app.icns")
		icon, readErr := os.ReadFile(iconPath)
		if readErr != nil {
			damaged = append(damaged, entry.Name()+": icon unreadable")
			continue
		}
		app.IconSHA256 = iconDigest(icon)
		if err := validateChromePWA(app); err != nil {
			damaged = append(damaged, entry.Name()+": "+err.Error())
			continue
		}
		live = append(live, liveChromePWA{chromePWA: app, Path: bundle, IconPath: iconPath})
	}
	apps := make([]chromePWA, len(live))
	for index := range live {
		apps[index] = live[index].chromePWA
	}
	canonical, normalized, err := canonicalChromePWAs(apps)
	if err != nil {
		return nil, nil, damaged, err
	}
	// normalizeChromePWAs rejects duplicate ids rather than dropping them, and
	// sorts by id, so sorting live the same way pairs the two by index.
	slices.SortFunc(live, func(left, right liveChromePWA) int {
		return strings.Compare(left.ID, right.ID)
	})
	for index := range live {
		live[index].chromePWA = normalized[index]
	}
	return canonical, live, damaged, nil
}

func chromePWADiff(saved []chromePWA, live []liveChromePWA) []string {
	savedByID := make(map[string]chromePWA, len(saved))
	liveByID := make(map[string]chromePWA, len(live))
	for _, app := range saved {
		savedByID[app.ID] = app
	}
	for _, app := range live {
		liveByID[app.ID] = app.chromePWA
	}
	var details []string
	for _, app := range live {
		if _, exists := savedByID[app.ID]; !exists {
			details = append(details, "Only on this Mac: "+app.Name)
		}
	}
	for _, app := range saved {
		if _, exists := liveByID[app.ID]; !exists {
			details = append(details, "Only in the saved backup: "+app.Name)
		}
	}
	return details
}

func (b Bidirectional) InspectChromePWAs() Resource {
	resource := Resource{ID: chromePWAsID, Name: chromePWAsName, Bidirectional: true}
	saved, savedApps, hasSaved, savedErr := b.chromePWASaved()
	live, liveApps, damaged, liveErr := b.chromePWALive()
	if savedErr != nil || liveErr != nil {
		resource.State = Unavailable
		resource.Summary = "PWA state unavailable"
		if savedErr != nil {
			resource.Checks = append(resource.Checks, no("saved PWA backup valid", savedErr.Error()))
		}
		if liveErr != nil {
			resource.Checks = append(resource.Checks, no("installed PWAs readable", liveErr.Error()))
		}
		return resource
	}
	for _, problem := range damaged {
		resource.Checks = append(resource.Checks, no("installed PWA readable", problem))
	}
	resource.Details = chromePWADiff(savedApps, liveApps)
	switch {
	case !hasSaved:
		resource.State = Uncaptured
		resource.Summary = "Chrome PWAs have not been captured"
		resource.Actions = []Action{Capture}
		resource.ActionLabels = map[Action]string{Capture: "Capture this Mac's PWAs"}
		return resource
	case len(liveApps) == 0 && len(savedApps) > 0:
		resource.State = SavedChanged
		resource.Summary = FormatCount(len(savedApps), "saved PWA is not restored", "saved PWAs are not restored")
		resource.Actions = []Action{Apply}
		resource.ActionLabels = map[Action]string{Apply: "Restore the saved PWAs"}
		return resource
	}
	baseline, hasBaseline, _ := b.Baselines.Load(resource.ID)
	chromePWAWords.offer(&resource, Classify(saved, live, baseline, hasBaseline))
	return resource
}

func (b Bidirectional) CaptureChromePWAs() error {
	_, live, damaged, err := b.chromePWALive()
	if err != nil {
		return err
	}
	// Backing up a collection with a bundle missing from it would drop that
	// PWA's saved icon and manifest entry.
	if len(damaged) > 0 {
		return fmt.Errorf("cannot back up an incomplete collection: %s", strings.Join(damaged, "; "))
	}
	apps := make([]chromePWA, len(live))
	for index, app := range live {
		icon, readErr := os.ReadFile(app.IconPath)
		if readErr != nil {
			return readErr
		}
		if err := atomicWrite(chromePWAIconPath(b.Paths, app.ID), icon, 0o644); err != nil {
			return err
		}
		apps[index] = app.chromePWA
	}
	_, normalized, err := canonicalChromePWAs(apps)
	if err != nil {
		return err
	}
	manifest, err := json.MarshalIndent(chromePWASnapshot{Schema: chromePWAsSchema, Apps: normalized}, "", "  ")
	if err != nil {
		return err
	}
	manifest = append(manifest, '\n')
	if err := atomicWrite(chromePWASnapshotPath(b.Paths), manifest, 0o644); err != nil {
		return err
	}
	keep := make(map[string]bool, len(normalized))
	for _, app := range normalized {
		keep[app.ID+".icns"] = true
	}
	entries, _ := os.ReadDir(chromePWAIconDir(b.Paths))
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".icns") && !keep[entry.Name()] {
			if err := os.Remove(filepath.Join(chromePWAIconDir(b.Paths), entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b Bidirectional) MarkChromePWAsIfCurrent() error {
	saved, _, hasSaved, savedErr := b.chromePWASaved()
	live, _, damaged, liveErr := b.chromePWALive()
	if savedErr != nil || liveErr != nil || len(damaged) > 0 || !hasSaved || !bytes.Equal(saved, live) {
		return fmt.Errorf("%s are not synchronized", chromePWAsName)
	}
	return b.Baselines.Save(chromePWAsID, saved)
}

func writeChromePWABundle(paths Paths, destination string, app chromePWA, template, loader, icon []byte) error {
	contents := filepath.Join(destination, "Contents")
	if err := os.MkdirAll(filepath.Join(contents, "MacOS"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(contents, "Resources", "en.lproj"), 0o755); err != nil {
		return err
	}
	values, err := decodePlist(template)
	if err != nil {
		return err
	}
	chromeVersion, _ := values["CFBundleShortVersionString"].(string)
	values["CFBundleIdentifier"] = chromeBundleID + ".app." + app.ID
	values["CFBundleName"] = app.Name
	values["CFBundleShortVersionString"] = ""
	values["CrAppModeShortcutID"] = app.ID
	values["CrAppModeShortcutName"] = app.Name
	values["CrAppModeShortcutURL"] = app.URL
	values["CrAppModeUserDataDir"] = paths.InHome("Library", "Application Support", "Google", "Chrome", "-", "Web Applications", "_crx_"+app.ID)
	values["CrAppModeIsAdhocSigned"] = true
	values["CrBundleIdentifier"] = chromeBundleID
	values["CrBundleVersion"] = chromeVersion
	values["LSHasLocalizedDisplayName"] = true
	values["NSHighResolutionCapable"] = true
	if len(app.Schemes) > 0 {
		values["CFBundleURLTypes"] = []any{map[string]any{
			"CFBundleURLName":    chromeBundleID + ".app." + app.ID,
			"CFBundleURLSchemes": app.Schemes,
		}}
	} else {
		delete(values, "CFBundleURLTypes")
	}
	info, err := plist.MarshalIndent(values, plist.XMLFormat, "\t")
	if err != nil {
		return err
	}
	stringsFile, err := plist.MarshalIndent(map[string]string{"CFBundleDisplayName": app.Name, "CFBundleName": app.Name}, plist.XMLFormat, "\t")
	if err != nil {
		return err
	}
	for _, file := range []struct {
		path string
		data []byte
		mode os.FileMode
	}{
		{filepath.Join(contents, "Info.plist"), info, 0o644},
		{filepath.Join(contents, "PkgInfo"), []byte("APPL????"), 0o644},
		{filepath.Join(contents, "MacOS", "app_mode_loader"), loader, 0o755},
		{filepath.Join(contents, "Resources", "app.icns"), icon, 0o644},
		{filepath.Join(contents, "Resources", "en.lproj", "InfoPlist.strings"), stringsFile, 0o644},
	} {
		if err := atomicWrite(file.path, file.data, file.mode); err != nil {
			return err
		}
	}
	return nil
}

func (e Applier) reconcileChromePWAs(action Action) error {
	switch action {
	case Capture:
		if err := e.Bidir.CaptureChromePWAs(); err != nil {
			return err
		}
		e.Log.OK("live PWAs backed up")
		return nil
	case Apply:
		return e.applyChromePWAs()
	default:
		return nil
	}
}

func (e Applier) applyChromePWAs() error {
	_, saved, hasSaved, err := e.Bidir.chromePWASaved()
	if err != nil {
		return err
	}
	if !hasSaved {
		return advisoryError{"no saved PWA backup; Chrome PWAs left untouched"}
	}
	_, live, _, err := e.Bidir.chromePWALive()
	if err != nil {
		return err
	}
	if chromePWACollectionsEqual(saved, live) {
		e.Log.OK("PWAs already current")
		return nil
	}
	if len(saved) == 0 {
		if !e.Runner.Exists("trash") {
			return fmt.Errorf("trash is required to restore PWAs")
		}
		for _, app := range live {
			if err := e.Live.Command("trash", app.Path); err != nil {
				return err
			}
		}
		e.Log.OK(FormatCount(len(live), "PWA change", "PWA changes") + " applied")
		return nil
	}
	template, err := os.ReadFile(chromePWATemplatePath())
	if err != nil {
		return fmt.Errorf("read Chrome app template: %w", err)
	}
	loader, err := os.ReadFile(chromePWALoaderPath())
	if err != nil {
		return fmt.Errorf("read Chrome app loader: %w", err)
	}
	if !e.Runner.Exists("codesign") || !e.Runner.Exists("trash") {
		return fmt.Errorf("codesign and trash are required to restore PWAs")
	}
	liveByID := make(map[string]liveChromePWA, len(live))
	for _, app := range live {
		liveByID[app.ID] = app
	}
	stage, err := os.MkdirTemp(chromePWALiveDir(e.Paths), ".config-pwas.*")
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(chromePWALiveDir(e.Paths), 0o755); err != nil {
			return err
		}
		stage, err = os.MkdirTemp(chromePWALiveDir(e.Paths), ".config-pwas.*")
	}
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	staged := make(map[string]string)
	for _, app := range saved {
		if current, exists := liveByID[app.ID]; exists && chromePWAEqual(current.chromePWA, app) {
			continue
		}
		icon, readErr := os.ReadFile(chromePWAIconPath(e.Paths, app.ID))
		if readErr != nil {
			return readErr
		}
		bundle := filepath.Join(stage, app.Name+".app")
		if err := writeChromePWABundle(e.Paths, bundle, app, template, loader, icon); err != nil {
			return err
		}
		if err := e.Live.Command("codesign", "--force", "--deep", "--sign", "-", bundle); err != nil {
			return err
		}
		staged[app.ID] = bundle
	}
	desiredIDs := make(map[string]bool, len(saved))
	for _, app := range saved {
		desiredIDs[app.ID] = true
	}
	extra := 0
	for _, app := range live {
		_, replacing := staged[app.ID]
		if !desiredIDs[app.ID] || replacing {
			if err := e.Live.Command("trash", app.Path); err != nil {
				return err
			}
			if !desiredIDs[app.ID] {
				extra++
			}
		}
	}
	changed := len(staged) + extra
	for _, app := range saved {
		bundle, exists := staged[app.ID]
		if !exists {
			continue
		}
		target := filepath.Join(chromePWALiveDir(e.Paths), app.Name+".app")
		if _, statErr := os.Stat(target); statErr == nil {
			if err := e.Live.Command("trash", target); err != nil {
				return err
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		if err := os.Rename(bundle, target); err != nil {
			return err
		}
	}
	e.Log.OK(FormatCount(changed, "PWA change", "PWA changes") + " applied")
	return nil
}
