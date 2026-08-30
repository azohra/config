package config

import (
	"context"
	"os"
	"slices"
	"testing"
)

type dockRunner struct {
	output string
}

func (r dockRunner) Run(_ context.Context, name string, args ...string) Result {
	if name == "dockutil" && slices.Equal(args, []string{"--list"}) {
		return Result{Stdout: r.output}
	}
	return Result{}
}

func (dockRunner) Exists(name string) bool { return name == "dockutil" }

func TestParseDock(t *testing.T) {
	output := "Safari\tfile:///System/Applications/Safari.app/\tpersistentApps\nDownloads\tfile:///Users/me/Downloads/\tpersistentOthers\nTerminal\tfile:///System/Applications/Utilities/Terminal.app/\trecentApps\nMail\tfile:///System/Applications/Mail.app/\tpersistentApps\n"
	got, err := parseDock(output)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/System/Applications/Safari.app", "/System/Applications/Mail.app"}
	if !slices.Equal(got, want) {
		t.Fatalf("parseDock() = %#v, want %#v", got, want)
	}
}

func TestPlanDock(t *testing.T) {
	a := "/Applications/A.app"
	b := "/Applications/B.app"
	c := "/Applications/C.app"
	d := "/Applications/D.app"
	tests := []struct {
		name  string
		saved []string
		live  []string
		want  []dockOperation
	}{
		{"current", []string{a, b}, []string{a, b}, nil},
		{"add", []string{a, b, c}, []string{a, c}, []dockOperation{{Action: "add", Path: b, Position: 2}}},
		{"remove", []string{a, c}, []string{a, b, c}, []dockOperation{{Action: "remove", Path: b}}},
		{"move", []string{a, b, c}, []string{b, a, c}, []dockOperation{{Action: "move", Path: a, Position: 1}}},
		{"mixed", []string{a, b, c}, []string{d, c, a}, []dockOperation{{Action: "remove", Path: d}, {Action: "move", Path: a, Position: 1}, {Action: "add", Path: b, Position: 2}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := planDock(tt.saved, tt.live); !slices.Equal(got, tt.want) {
				t.Fatalf("planDock() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestDockDiffUsesPlainLanguage(t *testing.T) {
	a := "/Applications/A.app"
	b := "/Applications/B.app"
	c := "/Applications/C.app"
	want := []string{"Only on this Mac: C.app", "Only in the saved layout: A.app"}
	if got := dockDiff([]string{a, b}, []string{b, c}); !slices.Equal(got, want) {
		t.Fatalf("dockDiff() = %#v, want %#v", got, want)
	}
	want = []string{"The same apps are in a different order"}
	if got := dockDiff([]string{a, b}, []string{b, a}); !slices.Equal(got, want) {
		t.Fatalf("dockDiff() = %#v, want %#v", got, want)
	}
}

func TestDockInitialCaptureCreatesTheTrackedSnapshot(t *testing.T) {
	paths := testPaths(t)
	app := paths.InHome("Applications", "Example.app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := dockRunner{output: "Example\tfile://" + app + "/\tpersistentApps\n"}
	bidir := NewBidirectional(paths, runner)

	resource := bidir.InspectDock()
	if resource.State != Uncaptured || resource.Failed() != 0 || !slices.Equal(resource.Actions, []Action{Capture}) {
		t.Fatalf("uncaptured Dock = %#v", resource)
	}
	if err := bidir.CaptureDock(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dockSnapshotPath(paths))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "~/Applications/Example.app\n" {
		t.Fatalf("captured Dock = %q", data)
	}
	if resource = bidir.InspectDock(); resource.State != Current {
		t.Fatalf("captured Dock resource = %#v", resource)
	}
}

func TestDockInitialCaptureCanTrackAnEmptyLayout(t *testing.T) {
	paths := testPaths(t)
	bidir := NewBidirectional(paths, dockRunner{})

	if err := bidir.CaptureDock(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dockSnapshotPath(paths))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "\n" {
		t.Fatalf("captured empty Dock = %q", data)
	}
	if resource := bidir.InspectDock(); resource.State != Current {
		t.Fatalf("captured empty Dock resource = %#v", resource)
	}
}

func TestDockCaptureCanAcceptAnUnavailableSavedApp(t *testing.T) {
	paths := testPaths(t)
	if err := atomicWrite(dockSnapshotPath(paths), []byte("/Applications/Missing.app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := paths.InHome("Applications", "Example.app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := dockRunner{output: "Example\tfile://" + app + "/\tpersistentApps\n"}
	bidir := NewBidirectional(paths, runner)
	resource := bidir.InspectDock()
	if !slices.Equal(resource.Actions, []Action{Capture}) {
		t.Fatalf("Dock with unavailable saved app = %#v", resource)
	}
	if err := bidir.CaptureDock(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dockSnapshotPath(paths))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "~/Applications/Example.app\n" {
		t.Fatalf("recaptured Dock = %q", data)
	}
}
