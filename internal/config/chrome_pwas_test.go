package config

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"howett.net/plist"
)

// chromePWAFromPlist reads a written bundle back the way Config identifies
// one, so a round trip can be compared against what was written.
func chromePWAFromPlist(data, icon []byte) (chromePWA, bool, error) {
	app, isPWA, err := chromePWAIdentity(data)
	if err != nil || !isPWA {
		return chromePWA{}, isPWA, err
	}
	app.IconSHA256 = iconDigest(icon)
	if err := validateChromePWA(app); err != nil {
		return chromePWA{}, true, err
	}
	return app, true, nil
}

func testChromePWA(name, id, shortcutURL string, icon []byte) chromePWA {
	return chromePWA{Name: name, ID: id, URL: shortcutURL, IconSHA256: iconDigest(icon)}
}

func testChromeTemplate(t *testing.T) []byte {
	t.Helper()
	data, err := plist.Marshal(map[string]any{
		"CFBundleExecutable":            "app_mode_loader",
		"CFBundleIconFile":              "app.icns",
		"CFBundlePackageType":           "APPL",
		"CFBundleShortVersionString":    "150.0.1.2",
		"CFBundleVersion":               "1.2",
		"CrAppModeShortcutID":           "@APP_MODE_SHORTCUT_ID@",
		"CrAppModeShortcutName":         "@APP_MODE_SHORTCUT_NAME@",
		"CrAppModeShortcutURL":          "@APP_MODE_SHORTCUT_URL@",
		"CrBundleIdentifier":            "@APP_MODE_BROWSER_BUNDLE_ID@",
		"NSAppleScriptEnabled":          true,
		"NSHighResolutionCapable":       true,
		"LSHasLocalizedDisplayName":     true,
		"CFBundleInfoDictionaryVersion": "6.0",
	}, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeTestLivePWA(t *testing.T, paths Paths, app chromePWA, icon []byte) string {
	t.Helper()
	bundle := filepath.Join(chromePWALiveDir(paths), app.Name+".app")
	if err := writeChromePWABundle(paths, bundle, app, testChromeTemplate(t), []byte("loader"), icon); err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestChromePWAValidationRejectsUnsafeMetadata(t *testing.T) {
	icon := []byte("icon")
	valid := testChromePWA("Gmail", "fmgjjmmmlfnkbppncabfkddbjimcfncm", "https://mail.google.com/", icon)
	for _, mutate := range []func(*chromePWA){
		func(app *chromePWA) { app.Name = "../Gmail" },
		func(app *chromePWA) { app.ID = "../../escape" },
		func(app *chromePWA) { app.URL = "file:///etc/passwd" },
		func(app *chromePWA) { app.Schemes = []string{"--bad"} },
		func(app *chromePWA) { app.IconSHA256 = "wrong" },
	} {
		app := valid
		mutate(&app)
		if err := validateChromePWA(app); err == nil {
			t.Fatalf("unsafe PWA accepted: %#v", app)
		}
	}
}

func TestChromePWABundleRoundTrip(t *testing.T) {
	paths := testPaths(t)
	icon := []byte("portable icon")
	app := testChromePWA("Fastmail", "nkbljeindhmekmppbpgebpjebkjbmfaj", "https://app.fastmail.com/mail/Inbox/", icon)
	app.Schemes = []string{"mailto"}
	bundle := writeTestLivePWA(t, paths, app, icon)

	info, err := os.ReadFile(filepath.Join(bundle, "Contents", "Info.plist"))
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, isPWA, err := chromePWAFromPlist(info, icon)
	if err != nil {
		t.Fatal(err)
	}
	if !isPWA || !chromePWAEqual(roundTrip, app) {
		t.Fatalf("round trip = %#v, %v; want %#v", roundTrip, isPWA, app)
	}
	values, err := decodePlist(info)
	if err != nil {
		t.Fatal(err)
	}
	wantUserData := paths.InHome("Library", "Application Support", "Google", "Chrome", "-", "Web Applications", "_crx_"+app.ID)
	if values["CrAppModeUserDataDir"] != wantUserData || values["CrBundleVersion"] != "150.0.1.2" {
		t.Fatalf("restored Chrome metadata = %#v", values)
	}
	loader, err := os.Stat(filepath.Join(bundle, "Contents", "MacOS", "app_mode_loader"))
	if err != nil {
		t.Fatal(err)
	}
	if loader.Mode().Perm() != 0o755 {
		t.Fatalf("loader mode = %v", loader.Mode())
	}
}

func TestChromePWAInspectionSupportsCaptureAndFreshMachineRestore(t *testing.T) {
	paths := testPaths(t)
	icon := []byte("icon")
	app := testChromePWA("Gmail", "fmgjjmmmlfnkbppncabfkddbjimcfncm", "https://mail.google.com/", icon)
	writeTestLivePWA(t, paths, app, icon)
	bidir := newBidirectional(paths, OSRunner{Dir: paths.Root})

	resource := bidir.InspectChromePWAs()
	if resource.State != Uncaptured || !slices.Equal(resource.Actions, []Action{Capture}) {
		t.Fatalf("uncaptured resource = %#v", resource)
	}
	if err := bidir.CaptureChromePWAs(); err != nil {
		t.Fatal(err)
	}
	resource = bidir.InspectChromePWAs()
	if resource.State != Current || len(resource.Actions) != 0 {
		t.Fatalf("captured resource = %#v", resource)
	}
	if err := os.RemoveAll(chromePWALiveDir(paths)); err != nil {
		t.Fatal(err)
	}
	resource = bidir.InspectChromePWAs()
	if resource.State != SavedChanged || !slices.Equal(resource.Actions, []Action{Apply}) {
		t.Fatalf("fresh-machine resource = %#v", resource)
	}
}

func TestChromePWAInitialCaptureTracksAnEmptyCollection(t *testing.T) {
	paths := testPaths(t)
	bidir := newBidirectional(paths, OSRunner{Dir: paths.Root})

	resource := bidir.InspectChromePWAs()
	if resource.State != Uncaptured || resource.Failed() != 0 || !slices.Equal(resource.Actions, []Action{Capture}) {
		t.Fatalf("uncaptured PWAs = %#v", resource)
	}
	if err := bidir.CaptureChromePWAs(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(chromePWASnapshotPath(paths))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\n  \"schema\": 1,\n  \"apps\": []\n}\n" {
		t.Fatalf("captured PWAs = %s", data)
	}
	if resource = bidir.InspectChromePWAs(); resource.State != Current {
		t.Fatalf("captured PWA resource = %#v", resource)
	}
}

func TestChromePWAEmptySnapshotCanRestoreAnEmptyCollection(t *testing.T) {
	paths := testPaths(t)
	runner := OSRunner{Dir: paths.Root}
	bidir := newBidirectional(paths, runner)
	if err := bidir.CaptureChromePWAs(); err != nil {
		t.Fatal(err)
	}

	icon := []byte("icon")
	app := testChromePWA("Gmail", "fmgjjmmmlfnkbppncabfkddbjimcfncm", "https://mail.google.com/", icon)
	bundle := writeTestLivePWA(t, paths, app, icon)
	fakeBin := t.TempDir()
	trash := filepath.Join(fakeBin, "trash")
	if err := os.WriteFile(trash, []byte("#!/bin/sh\nrm -rf -- \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	applier := Applier{
		Paths:  paths,
		Runner: runner,
		Live:   newLiveRunner(paths.Root),
		Log:    Logger{Out: io.Discard},
		Bidir:  bidir,
	}
	if err := applier.applyChromePWAs(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bundle); !os.IsNotExist(err) {
		t.Fatalf("restored empty PWA collection left %s: %v", bundle, err)
	}
}

func TestChromePWASavedBackupVerifiesIconContent(t *testing.T) {
	paths := testPaths(t)
	icon := []byte("icon")
	app := testChromePWA("Gmail", "fmgjjmmmlfnkbppncabfkddbjimcfncm", "https://mail.google.com/", icon)
	writeTestLivePWA(t, paths, app, icon)
	bidir := newBidirectional(paths, OSRunner{Dir: paths.Root})
	if err := bidir.CaptureChromePWAs(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chromePWAIconPath(paths, app.ID), []byte("different"), 0o644); err != nil {
		t.Fatal(err)
	}
	resource := bidir.InspectChromePWAs()
	if resource.State != Unavailable || resource.Failed() != 1 {
		t.Fatalf("corrupt backup resource = %#v", resource)
	}
}

func TestChromePWABundleBuiltFromInstalledChromeCanBeSigned(t *testing.T) {
	template, err := os.ReadFile(chromePWATemplatePath())
	if err != nil {
		t.Skip("Google Chrome is not installed")
	}
	loader, err := os.ReadFile(chromePWALoaderPath())
	if err != nil {
		t.Fatal(err)
	}
	paths := testPaths(t)
	icon := []byte("test icon")
	app := testChromePWA("Gmail", "fmgjjmmmlfnkbppncabfkddbjimcfncm", "https://mail.google.com/", icon)
	bundle := filepath.Join(t.TempDir(), app.Name+".app")
	if err := writeChromePWABundle(paths, bundle, app, template, loader, icon); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("codesign", "--force", "--deep", "--sign", "-", bundle).CombinedOutput(); err != nil {
		t.Fatalf("codesign: %v\n%s", err, output)
	}
	if output, err := exec.Command("codesign", "--verify", "--deep", "--strict", bundle).CombinedOutput(); err != nil {
		t.Fatalf("codesign verify: %v\n%s", err, output)
	}
}

// The restore branch stages a bundle, signs it, removes what it replaces, and
// renames the staged copy into place. It needs the installed Chrome it builds
// from, so it verifies against the real template and loader.
func TestChromePWARestoreInstallsTheSavedBundle(t *testing.T) {
	if _, err := os.Stat(chromePWATemplatePath()); err != nil {
		t.Skip("Google Chrome is not installed")
	}
	paths := testPaths(t)
	icon := []byte("icon")
	app := testChromePWA("Gmail", "fmgjjmmmlfnkbppncabfkddbjimcfncm", "https://mail.google.com/", icon)

	// Capture a live PWA, then remove it so the saved backup is the only copy.
	bundle := writeTestLivePWA(t, paths, app, icon)
	runner := OSRunner{Dir: paths.Root}
	bidir := newBidirectional(paths, runner)
	if err := bidir.CaptureChromePWAs(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(bundle); err != nil {
		t.Fatal(err)
	}

	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "trash"), []byte("#!/bin/sh\nrm -rf -- \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	applier := Applier{
		Paths:  paths,
		Runner: runner,
		Live:   newLiveRunner(paths.Root),
		Log:    Logger{Out: io.Discard},
		Bidir:  bidir,
	}
	if err := applier.applyChromePWAs(); err != nil {
		t.Fatal(err)
	}

	restored := filepath.Join(chromePWALiveDir(paths), app.Name+".app")
	info, err := os.ReadFile(filepath.Join(restored, "Contents", "Info.plist"))
	if err != nil {
		t.Fatalf("restore left no bundle at %s: %v", restored, err)
	}
	rebuilt, isPWA, err := chromePWAFromPlist(info, icon)
	if err != nil || !isPWA {
		t.Fatalf("restored bundle does not read back as a PWA: %v", err)
	}
	if !chromePWAEqual(rebuilt, app) {
		t.Fatalf("restored PWA = %+v, want %+v", rebuilt, app)
	}
	// Nothing may be left staged beside the bundle it installed.
	entries, err := os.ReadDir(chromePWALiveDir(paths))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".config-pwas") {
			t.Fatalf("restore left staging directory %s behind", entry.Name())
		}
	}
}

// writeTestBundle writes a bare .app beside the managed PWAs. A nil icon
// leaves the bundle without one, the way an ordinary Mac application dropped
// into this folder would be.
func writeTestBundle(t *testing.T, paths Paths, name string, info map[string]any, icon []byte) {
	t.Helper()
	contents := filepath.Join(chromePWALiveDir(paths), name+".app", "Contents")
	data, err := plist.Marshal(info, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(filepath.Join(contents, "Info.plist"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if icon != nil {
		if err := atomicWrite(filepath.Join(contents, "Resources", "app.icns"), icon, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Anything in this folder that is not a Chrome PWA is not Config's to manage.
// It must not cost the user the PWAs that are.
func TestChromePWAsSurviveAForeignBundle(t *testing.T) {
	paths := testPaths(t)
	icon := []byte("icon")
	app := testChromePWA("Gmail", "fmgjjmmmlfnkbppncabfkddbjimcfncm", "https://mail.google.com/", icon)
	writeTestLivePWA(t, paths, app, icon)

	// An ordinary application: real Info.plist, no Chrome shortcut, no app.icns.
	writeTestBundle(t, paths, "Foreign", map[string]any{"CFBundleName": "Foreign"}, nil)
	// A directory that only looks like a bundle.
	if err := os.MkdirAll(filepath.Join(chromePWALiveDir(paths), "Broken.app", "Contents"), 0o755); err != nil {
		t.Fatal(err)
	}

	bidir := newBidirectional(paths, OSRunner{Dir: paths.Root})
	_, live, damaged, err := bidir.chromePWALive()
	if err != nil {
		t.Fatalf("a foreign bundle broke the collection: %v", err)
	}
	if len(damaged) != 0 {
		t.Fatalf("a foreign bundle was reported as damaged: %v", damaged)
	}
	if len(live) != 1 || live[0].Name != "Gmail" {
		t.Fatalf("live PWAs = %+v, want only Gmail", live)
	}
	if err := bidir.CaptureChromePWAs(); err != nil {
		t.Fatalf("capture with a foreign bundle present: %v", err)
	}
	if resource := bidir.InspectChromePWAs(); resource.State != Current || resource.Failed() != 0 {
		t.Fatalf("resource with a foreign bundle = %#v", resource)
	}
}

// A PWA Config can identify but not trust is named, and the rest of the
// collection stays readable.
func TestChromePWAsNameADamagedBundleWithoutLosingTheRest(t *testing.T) {
	paths := testPaths(t)
	icon := []byte("icon")
	app := testChromePWA("Gmail", "fmgjjmmmlfnkbppncabfkddbjimcfncm", "https://mail.google.com/", icon)
	writeTestLivePWA(t, paths, app, icon)
	writeTestBundle(t, paths, "Damaged", map[string]any{
		"CrAppModeShortcutID":   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"CrAppModeShortcutName": "Damaged",
		"CrAppModeShortcutURL":  "not a url",
	}, icon)

	bidir := newBidirectional(paths, OSRunner{Dir: paths.Root})
	_, live, damaged, err := bidir.chromePWALive()
	if err != nil {
		t.Fatalf("a damaged PWA broke the collection: %v", err)
	}
	if len(live) != 1 || live[0].Name != "Gmail" {
		t.Fatalf("live PWAs = %+v, want only Gmail", live)
	}
	if len(damaged) != 1 || !strings.Contains(damaged[0], "Damaged.app") {
		t.Fatalf("damaged = %v, want the damaged bundle named", damaged)
	}

	resource := bidir.InspectChromePWAs()
	if resource.Failed() != 1 {
		t.Fatalf("a damaged PWA was not reported: %#v", resource)
	}
	if !strings.Contains(resource.Checks[0].Detail, "Damaged.app") {
		t.Fatalf("the failing check does not name the bundle: %+v", resource.Checks)
	}

	// Capture would write a manifest without the damaged app and delete the
	// icon saved for it, so it must refuse while the collection is incomplete.
	saved := chromePWAIconPath(paths, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err := atomicWrite(saved, icon, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := bidir.CaptureChromePWAs(); err == nil {
		t.Fatal("capture recorded an incomplete collection")
	}
	if _, err := os.Stat(saved); err != nil {
		t.Fatalf("a refused capture still removed the saved icon: %v", err)
	}
}

// Chrome owns what is inside a PWA bundle and rewrites it whenever a site
// changes its icon. Config tracks which PWAs are installed, so that rewrite
// is not drift and must not become a choice for the reader to make.
func TestChromePWAsIgnoreContentChromeOwns(t *testing.T) {
	paths := testPaths(t)
	app := testChromePWA("YouTube", "agimnkijcaahngcdmfeangaknmldooml", "https://www.youtube.com/", []byte("old icon"))
	writeTestLivePWA(t, paths, app, []byte("old icon"))
	bidir := newBidirectional(paths, OSRunner{Dir: paths.Root})
	if err := bidir.CaptureChromePWAs(); err != nil {
		t.Fatal(err)
	}
	if err := bidir.MarkChromePWAsIfCurrent(); err != nil {
		t.Fatal(err)
	}

	// Chrome rewrites the bundle with a new icon.
	refreshed := testChromePWA("YouTube", app.ID, app.URL, []byte("icon Chrome regenerated"))
	writeTestLivePWA(t, paths, refreshed, []byte("icon Chrome regenerated"))

	resource := bidir.InspectChromePWAs()
	if resource.State != Current {
		t.Fatalf("a Chrome icon rewrite became %s: %#v", resource.State, resource)
	}
	if resource.NeedsDecision() {
		t.Fatalf("a Chrome icon rewrite asked the reader to choose: %#v", resource)
	}

	// Installing a PWA is drift, because that is the fact Config tracks.
	added := testChromePWA("Gmail", "fmgjjmmmlfnkbppncabfkddbjimcfncm", "https://mail.google.com/", []byte("icon"))
	writeTestLivePWA(t, paths, added, []byte("icon"))
	if resource = bidir.InspectChromePWAs(); resource.State != LiveChanged {
		t.Fatalf("installing a PWA = %s, want live-changed: %#v", resource.State, resource)
	}
	if !slices.Contains(resource.Details, "Only on this Mac: Gmail") {
		t.Fatalf("the new PWA was not named: %+v", resource.Details)
	}
}

func TestChromePWAsUseThreeWayReconciliationOnceTheSidesHaveAgreed(t *testing.T) {
	// Removing every PWA from a Mac whose sides have agreed is a live change
	// someone may want to keep. Answering "the saved side changed" and
	// offering only a restore returned before the baseline was ever read.
	paths := testPaths(t)
	icon := []byte("icon")
	app := testChromePWA("Gmail", "fmgjjmmmlfnkbppncabfkddbjimcfncm", "https://mail.google.com/", icon)
	writeTestLivePWA(t, paths, app, icon)
	bidir := newBidirectional(paths, OSRunner{Dir: paths.Root})
	if err := bidir.CaptureChromePWAs(); err != nil {
		t.Fatal(err)
	}
	if err := bidir.MarkChromePWAsIfCurrent(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(chromePWALiveDir(paths)); err != nil {
		t.Fatal(err)
	}
	resource := bidir.InspectChromePWAs()
	if resource.State != LiveChanged {
		t.Fatalf("uninstalling every PWA reported as %q: %#v", resource.State, resource)
	}
	if !resource.Allows(Capture) {
		t.Fatalf("no way to keep this Mac's answer: %#v", resource.Actions)
	}
}

func TestChromePWARestoreTrashesABundleTheReplacementWillNotOverwrite(t *testing.T) {
	// A replacement that keeps its bundle name is trashed immediately before
	// its rename. One whose path the replacement will not take has to be
	// trashed up front, or the Mac keeps both copies.
	if _, err := os.Stat(chromePWATemplatePath()); err != nil {
		t.Skip("Google Chrome is not installed")
	}
	paths := testPaths(t)
	icon := []byte("icon")
	app := testChromePWA("Gmail", "fmgjjmmmlfnkbppncabfkddbjimcfncm", "https://mail.google.com/", icon)

	bundle := writeTestLivePWA(t, paths, app, icon)
	runner := OSRunner{Dir: paths.Root}
	bidir := newBidirectional(paths, runner)
	if err := bidir.CaptureChromePWAs(); err != nil {
		t.Fatal(err)
	}
	// The saved snapshot names the same PWA differently, so the replacement
	// lands beside the installed bundle rather than on top of it.
	snapshot, err := os.ReadFile(chromePWASnapshotPath(paths))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(snapshot, []byte(`"name": "Gmail"`)) {
		t.Fatalf("saved snapshot does not name the PWA as expected: %s", snapshot)
	}
	renamedSnapshot := bytes.ReplaceAll(snapshot, []byte(`"name": "Gmail"`), []byte(`"name": "Mail"`))
	if err := atomicWrite(chromePWASnapshotPath(paths), renamedSnapshot, 0o600); err != nil {
		t.Fatal(err)
	}

	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "trash"), []byte("#!/bin/sh\nrm -rf -- \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	applier := Applier{
		Paths: paths, Runner: runner, Live: newLiveRunner(paths.Root),
		Log: Logger{Out: io.Discard}, Bidir: bidir,
	}
	if err := applier.applyChromePWAs(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bundle); !os.IsNotExist(err) {
		t.Fatalf("the bundle the replacement did not overwrite survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(chromePWALiveDir(paths), "Mail.app")); err != nil {
		t.Fatalf("the saved bundle was not installed under its own name: %v", err)
	}
}
