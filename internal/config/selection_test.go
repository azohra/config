package config

import (
	"slices"
	"strings"
	"testing"
)

func TestValidateSelections(t *testing.T) {
	report := Report{Resources: []Resource{
		{ID: "setup", Name: "Machine setup", State: Drift, Actions: []Action{Apply}},
		{ID: "dock", Name: "Dock", State: LiveChanged, Bidirectional: true, Actions: []Action{Capture, Apply}},
	}}
	tests := []struct {
		name       string
		selections []Selection
		wantError  bool
	}{
		{"valid", []Selection{{ID: "setup", Action: Apply}, {ID: "dock", Action: Capture}}, false},
		{"duplicate", []Selection{{ID: "setup", Action: Apply}, {ID: "setup", Action: Apply}}, true},
		{"unknown", []Selection{{ID: "missing", Action: Apply}}, true},
		{"wrong direction", []Selection{{ID: "setup", Action: Capture}}, true},
		{"read only", []Selection{{ID: "snapshot", Action: Apply}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSelections(report, tt.selections)
			if (err != nil) != tt.wantError {
				t.Fatalf("ValidateSelections() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

// A failed check, a resource waiting on a choice, and one never captured are
// three different things, and the status line counts them separately.
func TestCountsSeparatesDecisionsFromFailures(t *testing.T) {
	report := Report{Resources: []Resource{
		{Checks: []Check{{Label: "git", OK: false}, {Label: "mise", OK: true}}},
		{Bidirectional: true, State: LiveChanged, Actions: []Action{Capture, Apply}},
		{State: Uncaptured, Actions: []Action{Capture}},
	}, Snapshot: SnapshotStatus{Dirty: 1, Upstream: "origin/main"}}
	failures, decisions, advisories := report.Counts()
	if failures != 1 || decisions != 1 || advisories != 1 {
		t.Fatalf("Counts() = %d, %d, %d", failures, decisions, advisories)
	}
	if warnings := report.Snapshot.Warnings(); warnings != 1 {
		t.Fatalf("Snapshot.Warnings() = %d", warnings)
	}
}

func TestDecodeSelectionsRejectsMalformedPlans(t *testing.T) {
	tests := []struct {
		name      string
		encoded   string
		wantError bool
	}{
		{"apply", `[{"id":"dock","action":"apply"}]`, false},
		{"capture", `[{"id":"dock","action":"capture"}]`, false},
		{"empty plan", `[]`, false},
		{"not JSON", `dock`, true},
		{"missing id", `[{"action":"apply"}]`, true},
		{"empty id", `[{"id":"","action":"apply"}]`, true},
		{"skip is not an instruction", `[{"id":"dock","action":"skip"}]`, true},
		{"unknown action", `[{"id":"dock","action":"delete"}]`, true},
		{"one bad entry rejects the plan", `[{"id":"dock","action":"apply"},{"id":"x","action":""}]`, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeSelections(test.encoded)
			if (err != nil) != test.wantError {
				t.Fatalf("DecodeSelections(%s) error = %v, wantError %v", test.encoded, err, test.wantError)
			}
		})
	}
}

// The plan crosses a process boundary as one argv element, so what the parent
// encodes must be exactly what the child acts on.
func TestSelectionsSurviveTheProcessBoundary(t *testing.T) {
	want := []Selection{{ID: "setup", Action: Apply}, {ID: "chrome-pwas", Action: Capture}}
	encoded, err := EncodeSelections(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeSelections(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

// Save runs this before it commits. A failed check or an unresolved
// bidirectional choice must stop the snapshot, and must say which resource.
func TestPreflightErrorStopsOnFailuresAndUnresolvedChoices(t *testing.T) {
	clean := Report{Resources: []Resource{
		{Name: "Example App", Checks: []Check{{Label: "Preference backup valid", OK: true}}},
		{Name: "Dock", State: Current, Bidirectional: true},
	}}
	if err := clean.PreflightError(); err != nil {
		t.Fatalf("a converged machine failed preflight: %v", err)
	}

	failing := Report{Resources: []Resource{
		{Name: "Example App", Checks: []Check{{Label: "Saved settings valid"}}},
	}}
	err := failing.PreflightError()
	if err == nil || !strings.Contains(err.Error(), "Example App: Saved settings valid") {
		t.Fatalf("failed check preflight error = %v", err)
	}

	undecided := Report{Resources: []Resource{
		{Name: "Dock", State: Conflict, Bidirectional: true, Actions: []Action{Apply, Capture}},
	}}
	err = undecided.PreflightError()
	if err == nil || !strings.Contains(err.Error(), "Dock: unresolved conflict") {
		t.Fatalf("unresolved choice preflight error = %v", err)
	}
}

// A snapshot records Config-owned state. Machine setup converges live
// settings and writes nothing into the repository, so mise needing attention
// — a checkout behind its remote, a package not installed — must not stop a
// backup of the Dock, the PWAs, and the saved preferences.
func TestPreflightErrorDoesNotLetMachineSetupBlockASnapshot(t *testing.T) {
	report := Report{Resources: []Resource{
		authoritativeResource(setupID, setupName, []Check{
			no("mise bootstrap state needs attention", "repos"),
		}),
		{ID: dockID, Name: dockName, State: Current, Bidirectional: true},
	}}
	if err := report.PreflightError(); err != nil {
		t.Fatalf("machine setup blocked a snapshot: %v", err)
	}

	// A resource that owns snapshot content still blocks: its saved artifact
	// is what the commit would record.
	corrupt := Report{Resources: []Resource{
		authoritativeResource(setupID, setupName, nil),
		{ID: chromePWAsID, Name: chromePWAsName, State: Unavailable, Checks: []Check{
			no("saved PWA backup valid", "icon digest mismatch"),
		}},
	}}
	err := corrupt.PreflightError()
	if err == nil || !strings.Contains(err.Error(), chromePWAsName) {
		t.Fatalf("a corrupt saved backup did not block the snapshot: %v", err)
	}
}
