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
