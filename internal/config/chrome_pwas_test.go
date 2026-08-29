package config

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"howett.net/plist"
)

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
	bidir := NewBidirectional(paths, OSRunner{Dir: paths.Root})

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
	bidir := NewBidirectional(paths, OSRunner{Dir: paths.Root})

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
	bidir := NewBidirectional(paths, runner)
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
		Live:   NewLiveRunner(paths.Root),
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
	bidir := NewBidirectional(paths, OSRunner{Dir: paths.Root})
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
