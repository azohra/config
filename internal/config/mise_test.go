package config

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"testing"
	"time"
)

func TestMiseVersionContract(t *testing.T) {
	for _, test := range []struct {
		output  string
		minimum string
		want    bool
	}{
		{"2026.8.14 macos-arm64", "2026.8.14", true},
		{"mise 2026.9.0", "2026.8.14", true},
		{"v2026.8.13", "2026.8.14", false},
		{"2025.12.99", "2026.8.14", false},
		{"not a version", "2026.8.14", false},
	} {
		if got := miseVersionAtLeast(test.output, test.minimum); got != test.want {
			t.Errorf("miseVersionAtLeast(%q, %q) = %v, want %v", test.output, test.minimum, got, test.want)
		}
	}
}

type bootstrapStateRunner struct {
	status          Result
	statusHasBudget *bool
}

func (r bootstrapStateRunner) Run(ctx context.Context, name string, args ...string) Result {
	switch {
	case name == "mise" && slices.Equal(args, []string{"--version"}):
		return Result{Stdout: minimumMiseVersion}
	case name == "mise" && slices.Equal(args, []string{"bootstrap", "status", "--missing"}):
		if r.statusHasBudget != nil {
			deadline, ok := ctx.Deadline()
			*r.statusHasBudget = ok && time.Until(deadline) > 4*time.Minute
		}
		return r.status
	default:
		return Result{Err: fmt.Errorf("unexpected command: %s %v", name, args)}
	}
}
func (bootstrapStateRunner) Exists(string) bool { return true }

func TestMiseChecksConsumeTheAggregateBootstrapContract(t *testing.T) {
	drifted := Result{Err: exec.Command("/usr/bin/false").Run()}
	tests := []struct {
		name   string
		result Result
		ok     bool
		label  string
	}{
		{"current", Result{}, true, "mise bootstrap state"},
		{"drifted", drifted, false, "mise bootstrap state needs attention"},
		{"broken", Result{Err: errors.New("mise failed")}, false, "mise bootstrap unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checks := (Inspector{Runner: bootstrapStateRunner{status: test.result}}).miseChecks()
			check := checks[1]
			if check.OK != test.ok || check.Label != test.label {
				t.Fatalf("miseChecks() = %+v", checks)
			}
		})
	}
}

func TestMiseStatusAllowsAFullMachineInspection(t *testing.T) {
	hasBudget := false
	checks := (Inspector{Runner: bootstrapStateRunner{statusHasBudget: &hasBudget}}).miseChecks()
	if !hasBudget || !checks[1].OK {
		t.Fatalf("miseChecks() = %+v, has budget = %t", checks, hasBudget)
	}
}
