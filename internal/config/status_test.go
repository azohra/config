package config

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteStatusUsesNaturalCounts(t *testing.T) {
	report := Report{
		Snapshot: SnapshotStatus{Branch: "main", Commit: "abc1234", Dirty: 1, Upstream: "origin/main", Destination: "origin/main"},
	}
	var output bytes.Buffer
	WriteStatus(&output, report)
	for _, want := range []string{"1 changed file", "0 failures", "0 decisions", "1 warning"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("status missing %q:\n%s", want, output.String())
		}
	}
	if strings.Contains(output.String(), "(s)") {
		t.Fatalf("status contains mechanical pluralization:\n%s", output.String())
	}
}

func TestStatusHeaderAgreesWithEverySurface(t *testing.T) {
	// The header derived the headline from Dirty alone, so a checkout holding
	// unpushed commits read as clean here while the terminal called it
	// unpushed and Save refused to call it done.
	var out strings.Builder
	report := Report{Snapshot: SnapshotStatus{
		Branch: "main", Commit: "abc1234", Upstream: "origin/main", Ahead: 2,
	}}
	WriteStatus(&out, report)
	header := strings.SplitN(out.String(), "\n", 3)[1]
	if strings.Contains(header, "clean") {
		t.Fatalf("a checkout with unpushed commits reported clean: %q", header)
	}
	for _, part := range report.Snapshot.PendingParts() {
		if !strings.Contains(header, part) {
			t.Errorf("header %q omits %q", header, part)
		}
	}

	out.Reset()
	WriteStatus(&out, Report{Snapshot: SnapshotStatus{Branch: "main", Commit: "abc1234", Upstream: "origin/main"}})
	if header := strings.SplitN(out.String(), "\n", 3)[1]; !strings.Contains(header, "clean") {
		t.Fatalf("a settled checkout did not report clean: %q", header)
	}
}
