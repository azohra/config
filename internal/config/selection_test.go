package config

import "testing"

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

func TestCountsSeparatesDecisionsFromFailures(t *testing.T) {
	report := Report{Resources: []Resource{
		{Checks: []Check{{OK: false, Severity: Failure}}},
		{Checks: []Check{{OK: false, Severity: Advisory}}},
		{Bidirectional: true, State: LiveChanged, Actions: []Action{Capture, Apply}},
		{State: Uncaptured, Actions: []Action{Capture}},
	}, Snapshot: SnapshotStatus{Dirty: 1, Upstream: "origin/main"}}
	failures, decisions, advisories := report.Counts()
	if failures != 1 || decisions != 1 || advisories != 2 {
		t.Fatalf("Counts() = %d, %d, %d", failures, decisions, advisories)
	}
	if warnings := report.Snapshot.Warnings(); warnings != 1 {
		t.Fatalf("Snapshot.Warnings() = %d", warnings)
	}
}
