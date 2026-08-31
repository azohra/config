package ui

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	config "github.com/azohra/config/internal/config"
)

func TestDashboardShowsOnlyAvailableActions(t *testing.T) {
	m := Model{
		report: config.Report{
			Resources: []config.Resource{{ID: "setup", Name: "Machine setup", State: config.Current}},
			Snapshot:  config.SnapshotStatus{Upstream: "origin/main"},
		},
		width: 80, height: 24,
	}
	view := m.renderDashboard()
	for _, present := range []string{
		"Configuration matches",
		"cleanup runs on demand",
		"Update software",
		"Config, mise, tools, and packages",
		"Update repositories",
		"fetch and fast-forward clean checkouts",
		"Clean up",
		"unused tools and Config state",
		"Inspect configuration",
		"Quit",
	} {
		if !strings.Contains(view, present) {
			t.Fatalf("clean dashboard missing %q:\n%s", present, view)
		}
	}
	for _, absent := range []string{"Review changes", "Save snapshot", "Machine setup"} {
		if strings.Contains(view, absent) {
			t.Fatalf("clean dashboard exposes %q:\n%s", absent, view)
		}
	}

	m.report.Resources[0].State = config.Drift
	m.report.Resources[0].Actions = []config.Action{config.Apply}
	m.report.Snapshot.Dirty = 1
	view = m.renderDashboard()
	for _, present := range []string{"Review changes", "Save snapshot", "Machine setup", "1 changed file"} {
		if !strings.Contains(view, present) {
			t.Fatalf("changed dashboard missing %q:\n%s", present, view)
		}
	}
}

func TestDashboardShowsBidirectionalAttentionWithoutRepeatingSnapshot(t *testing.T) {
	m := Model{
		report: config.Report{
			Resources: []config.Resource{
				{ID: "example-app", Name: "Example App", State: config.Current, Bidirectional: true},
				{ID: "dock", Name: "Dock", State: config.LiveChanged, Summary: "this Mac changed", Bidirectional: true, Actions: []config.Action{config.Capture, config.Apply}},
			},
			Snapshot: config.SnapshotStatus{Dirty: 7, Upstream: "origin/main", Destination: "origin/main"},
		},
		width: 80, height: 24,
	}
	view := m.renderDashboard()
	for _, present := range []string{"Your choice is needed", "Needs attention", "Dock", "Review changes", "Save snapshot"} {
		if !strings.Contains(view, present) {
			t.Fatalf("dashboard missing %q:\n%s", present, view)
		}
	}
	if strings.Contains(view, "Example App") || strings.Count(view, "7 changed files") != 1 {
		t.Fatalf("dashboard repeats current or snapshot state:\n%s", view)
	}
}

func TestDashboardBoundsLargeAttentionLists(t *testing.T) {
	var resources []config.Resource
	for index := range 9 {
		resources = append(resources, config.Resource{
			ID: fmt.Sprintf("resource-%d", index), Name: fmt.Sprintf("Resource %d", index),
			State: config.Drift, Actions: []config.Action{config.Apply},
		})
	}
	m := Model{report: config.Report{Resources: resources, Snapshot: config.SnapshotStatus{Dirty: 1}}, width: 80, height: 24}
	view := m.renderDashboard()
	if lipgloss.Height(view) > m.height || !strings.Contains(view, "more resources") {
		t.Fatalf("dashboard overflowed or omitted summary (%d lines):\n%s", lipgloss.Height(view), view)
	}
}

func TestCleanupPreviewBoundsLongPlans(t *testing.T) {
	var lines []string
	for index := range 30 {
		lines = append(lines, fmt.Sprintf("candidate %d", index))
	}
	m := Model{
		screen: screenPrune, prunePreview: strings.Join(lines, "\n"), pruneHasWork: true,
		width: 80, height: 24,
	}
	view := m.renderPrune()
	if lipgloss.Height(view) > m.height || !strings.Contains(view, "↑/↓ scroll") || !strings.Contains(view, "Prune these items") {
		t.Fatalf("cleanup preview overflowed or lost its controls (%d lines):\n%s", lipgloss.Height(view), view)
	}
}

func TestInventoryShowsCombinedConcreteEvidence(t *testing.T) {
	m := Model{
		report: config.Report{Resources: []config.Resource{
			{ID: "setup", Name: "Machine setup", State: config.Current, Summary: "12 checks current", Checks: []config.Check{{Label: "mise 2026.8.14", OK: true}, {Label: "mise bootstrap state", OK: true}}},
			{ID: "example-app", Name: "Example App", State: config.Current, Summary: "this Mac matches the saved settings", Bidirectional: true},
			{ID: "dock", Name: "Dock", State: config.Current, Summary: "this Mac matches the saved layout", Bidirectional: true},
		}},
		width: 100, height: 30,
	}
	view := m.renderInventory()
	for _, present := range []string{"3 resources", "Machine setup", "mise 2026.8.14", "mise bootstrap state", "Example App", "Dock"} {
		if !strings.Contains(view, present) {
			t.Fatalf("inventory missing %q:\n%s", present, view)
		}
	}
	for _, absent := range []string{"SSH & GitHub", "Repository", "Snapshot"} {
		if strings.Contains(view, absent) {
			t.Fatalf("inventory exposes retired resource %q:\n%s", absent, view)
		}
	}
}

func TestPlanShowsInlineChoiceAndEvidence(t *testing.T) {
	m := Model{
		choices: []planChoice{{
			resource: config.Resource{
				ID: "dock", Name: "Dock", Bidirectional: true, Details: []string{"Only on this Mac: Signal.app"},
				ActionLabels: map[config.Action]string{config.Capture: "Save this Mac's layout", config.Apply: "Restore the saved layout"},
			},
			options: []config.Action{config.Skip, config.Capture, config.Apply},
		}},
		width: 80, height: 24,
	}
	view := m.renderPlan()
	for _, present := range []string{"Dock", "Decide later", "Only on this Mac: Signal.app", "←/→ choose"} {
		if !strings.Contains(view, present) {
			t.Fatalf("plan missing %q:\n%s", present, view)
		}
	}
	if strings.Contains(view, "Run 1 change") {
		t.Fatalf("skipped plan exposes run row:\n%s", view)
	}
	m.choices[0].choice = 1
	view = m.renderPlan()
	for _, present := range []string{"Save this Mac's layout", "Run 1 change"} {
		if !strings.Contains(view, present) {
			t.Fatalf("selected plan missing %q:\n%s", present, view)
		}
	}
}

func TestDashboardPresentsOperationResultAsBanner(t *testing.T) {
	m := Model{
		last:   operationResult{label: "Apply"},
		report: config.Report{Snapshot: config.SnapshotStatus{Dirty: 1}},
		width:  80, height: 24,
	}
	view := m.renderDashboard()
	if !strings.Contains(view, "Apply complete") || !strings.Contains(view, "Save snapshot") {
		t.Fatalf("dashboard missing result or next action:\n%s", view)
	}
}

// The pane colors what the Logger wrote, so drive a real Logger: a glyph the
// producer stops emitting must fail here rather than silently lose its color.
// Coloring never alters the text it paints, and the command output sampled here
// keeps every byte.
func TestOperationTailColorsStepLines(t *testing.T) {
	var narration bytes.Buffer
	log := config.Logger{Out: &narration}
	log.Info("checking machine state")
	log.OK("machine state valid")
	log.Warn("commit remains local")
	log.Error("push rejected")
	steps := strings.Split(strings.TrimRight(narration.String(), "\n"), "\n")
	untouched := []string{"[check] ~/.gitconfig  symlink  applied", " 1 file changed, 1 insertion(+)", "  1 file changed", "Snapshot"}

	m := Model{width: 94}
	lines := strings.Split(m.operationTail(strings.Join(append(steps, untouched...), "\n"), 20), "\n")
	if len(lines) != len(steps)+len(untouched) {
		t.Fatalf("operationTail returned %d lines for %d", len(lines), len(steps)+len(untouched))
	}
	for index, want := range steps {
		got := lines[index]
		if got == want {
			t.Fatalf("step line %q went uncolored", want)
		}
		if stripped := ansi.Strip(got); stripped != want {
			t.Fatalf("coloring changed the text: %q became %q", want, stripped)
		}
	}
	for offset, want := range untouched {
		if got := lines[len(steps)+offset]; got != want {
			t.Fatalf("command output line %q became %q", want, got)
		}
	}
}
