package ui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	config "github.com/azohra/config/internal/config"
)

func TestDashboardShowsOnlyAvailableActions(t *testing.T) {
	m := Model{
		report: config.Report{
			Resources: []config.Resource{{ID: "mise", Name: "Mise", State: config.Current}},
			Snapshot:  config.SnapshotStatus{Upstream: "origin/main"},
		},
		width: 80, height: 24,
	}
	view := m.renderDashboard()
	for _, present := range []string{
		"Configuration matches",
		"cleanup runs on demand",
		"Update software",
		"select to check software",
		"Update repositories",
		"select to check repositories",
		"Clean up",
		"unused tools and Config state",
		"Inspect configuration",
		"Quit",
	} {
		if !strings.Contains(view, present) {
			t.Fatalf("clean dashboard missing %q:\n%s", present, view)
		}
	}
	for _, absent := range []string{"Review changes", "Save snapshot", "Mise"} {
		if strings.Contains(view, absent) {
			t.Fatalf("clean dashboard exposes %q:\n%s", absent, view)
		}
	}

	m.report.Resources[0].State = config.Drift
	m.report.Resources[0].Actions = []config.Action{config.Apply}
	m.report.Snapshot.Dirty = 1
	view = m.renderDashboard()
	for _, present := range []string{"Review changes", "Save snapshot", "Mise", "1 changed file"} {
		if !strings.Contains(view, present) {
			t.Fatalf("changed dashboard missing %q:\n%s", present, view)
		}
	}
}

func TestDashboardSummarizesBackgroundUpdateDiscovery(t *testing.T) {
	m := Model{
		report:        config.Report{Snapshot: config.SnapshotStatus{Upstream: "origin/main"}},
		overviewReady: true,
		updateOverview: config.UpdatePlan{
			Scope: config.UpdateAll, CheckedAt: time.Date(2026, 9, 2, 15, 4, 0, 0, time.Local),
			Groups: []config.UpdateGroup{
				{Name: "Config", Scope: config.UpdateAll, State: config.UpdateCurrent},
				{Name: "Tools", Scope: config.UpdateSoftware, State: config.UpdateAvailable},
				{Name: "Packages", Scope: config.UpdateSoftware, State: config.UpdatePending},
				{Name: "Repositories", Scope: config.UpdateRepositories, State: config.UpdatePending},
			},
		},
		width: 100, height: 24,
	}
	view := ansi.Strip(m.renderDashboard())
	for _, want := range []string{"1 available update · 1 checked when run · 3:04 PM", "1 checked when run · 3:04 PM"} {
		if !strings.Contains(view, want) {
			t.Fatalf("dashboard missing %q:\n%s", want, view)
		}
	}
}

func TestUpdatePreviewMakesTheMutationExplicit(t *testing.T) {
	m := Model{
		screen:      screenUpdate,
		updateScope: config.UpdateSoftware,
		updatePreview: config.UpdatePlan{
			Scope:  config.UpdateSoftware,
			Groups: []config.UpdateGroup{{Name: "Packages", Scope: config.UpdateSoftware, State: config.UpdatePending, Summary: "checked when run"}},
		},
		width: 80, height: 24,
	}
	view := ansi.Strip(m.renderUpdate())
	for _, want := range []string{"Review first", "Packages", "checked when run", "Check and update software", "enter run"} {
		if !strings.Contains(view, want) {
			t.Fatalf("update preview missing %q:\n%s", want, view)
		}
	}
}

func TestSoftwareOverviewDoesNotClaimRepositoryFreshness(t *testing.T) {
	m := Model{
		overviewReady: true,
		updateOverview: config.UpdatePlan{
			Scope:  config.UpdateSoftware,
			Groups: []config.UpdateGroup{{Name: "Config", Scope: config.UpdateAll, State: config.UpdateCurrent}},
		},
	}
	if summary := m.updateActionSummary(config.UpdateRepositories, "select to check repositories"); summary != "select to check repositories" {
		t.Fatalf("repository summary = %q", summary)
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
	for _, present := range []string{"Your choice is needed", "Not current", "Dock", "Review changes", "Save snapshot"} {
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
			{ID: "mise", Name: "Mise", State: config.Current, Summary: "12 checks current", Checks: []config.Check{{Label: "mise 2026.9.1", OK: true}, {Label: "mise bootstrap state", OK: true}}},
			{ID: "example-app", Name: "Example App", State: config.Current, Summary: "this Mac matches the saved settings", Bidirectional: true},
			{ID: "dock", Name: "Dock", State: config.Current, Summary: "this Mac matches the saved layout", Bidirectional: true},
		}},
		width: 100, height: 30,
	}
	view := m.renderInventory()
	for _, present := range []string{"3 resources", "Mise", "mise 2026.9.1", "mise bootstrap state", "Example App", "Dock"} {
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

// Typed Config progress is durable and colored. Provider output contributes
// only the current activity line until the user asks for details.
func TestRunningOperationSeparatesProgressFromProviderActivity(t *testing.T) {
	events := []config.OperationEvent{
		{Kind: config.OperationInfo, Text: "checking machine state"},
		{Kind: config.OperationOK, Text: "machine state valid"},
		{Kind: config.OperationWarn, Text: "commit remains local"},
		{Kind: config.OperationError, Text: "push rejected"},
	}
	steps := []string{"  → checking machine state", "  ✓ machine state valid", "  ! commit remains local", "  ✗ push rejected"}
	diagnostics := []string{"[check] ~/.gitconfig  symlink  applied", " 1 file changed, 1 insertion(+)", "Snapshot"}
	log := newOperationLog()
	for _, event := range events {
		log.Append(event)
	}
	log.Append(config.OperationEvent{Kind: config.OperationOutput, Text: strings.Join(diagnostics, "\n") + "\n"})
	colored := log.progress.Lines(86, "progress line")
	for index, want := range steps {
		if got := colored[index]; got == want || ansi.Strip(got) != want {
			t.Fatalf("progress color changed %q into %q", want, got)
		}
	}

	m := Model{width: 94, height: 30, screen: screenRunning, operation: operation{label: "Update", log: log}}
	view := m.renderRunning()
	plain := ansi.Strip(view)
	for _, want := range steps {
		if !strings.Contains(plain, want) {
			t.Fatalf("progress missing %q:\n%s", want, plain)
		}
	}
	if !strings.Contains(plain, "Now  Snapshot") || strings.Contains(plain, diagnostics[0]) {
		t.Fatalf("default view did not compact provider activity:\n%s", plain)
	}
	if lipgloss.Height(view) > m.height {
		t.Fatalf("running progress height=%d, terminal=%d", lipgloss.Height(view), m.height)
	}

	m.showDiagnostics = true
	details := ansi.Strip(m.renderRunning())
	for _, want := range diagnostics {
		if !strings.Contains(details, want) {
			t.Fatalf("details missing %q:\n%s", want, details)
		}
	}
	if strings.Contains(details, steps[0]) {
		t.Fatalf("details mixed progress into provider output:\n%s", details)
	}
	if lipgloss.Height(m.renderRunning()) > m.height {
		t.Fatalf("running details exceeded terminal height")
	}
}

func TestResultDefaultsToProgressAndTogglesDetails(t *testing.T) {
	log := newOperationLog()
	log.Append(config.OperationEvent{Kind: config.OperationOK, Text: "packages current"})
	log.Append(config.OperationEvent{Kind: config.OperationOutput, Text: "provider detail\n"})
	m := Model{
		width: 80, height: 24, screen: screenResult,
		last: operationResult{label: "Update", log: log, finishedAt: time.Now(), duration: 3 * time.Second},
	}

	progress := ansi.Strip(m.renderResult())
	if !strings.Contains(progress, "packages current") || !strings.Contains(progress, "3s") || strings.Contains(progress, "provider detail") {
		t.Fatalf("result progress =\n%s", progress)
	}
	next, _ := m.updateResult(tea.KeyPressMsg{Code: 'd', Text: "d"})
	details := ansi.Strip(next.(Model).renderResult())
	if !strings.Contains(details, "provider detail") || strings.Contains(details, "packages current") {
		t.Fatalf("result details =\n%s", details)
	}
	if lipgloss.Height(next.(Model).renderResult()) > m.height {
		t.Fatalf("result details exceeded terminal height")
	}
}

func TestDashboardAllClearNamesTheDeclaredBranch(t *testing.T) {
	// A machine repository that declares any other branch was told its
	// snapshot agrees with a branch it does not use.
	report := config.Report{Snapshot: config.SnapshotStatus{
		Upstream: "origin/machine", Destination: "origin/machine",
	}}
	_, detail := dashboardHealth(report)
	if !strings.Contains(detail, "origin/machine") {
		t.Fatalf("all-clear detail = %q", detail)
	}
	if strings.Contains(detail, "origin/main") {
		t.Fatalf("all-clear detail names a branch the machine does not use: %q", detail)
	}
}

func TestDashboardListsWhatSymbolCallsUnsettled(t *testing.T) {
	// internal/config documents Resource.Symbol as the one state-to-severity
	// answer every status surface gives; the dashboard used to define a third
	// rule beside it and Report.NeedsAttention.
	report := config.Report{Resources: []config.Resource{
		{ID: "settled", Name: "Settled", State: config.Current},
		{ID: "uncaptured", Name: "Uncaptured", State: config.Uncaptured, Actions: []config.Action{config.Capture}},
		{ID: "failing", Name: "Failing", State: config.Current, Checks: []config.Check{{Label: "probe", OK: false}}},
		{ID: "drifted", Name: "Drifted", State: config.Drift},
	}}
	listed := unsettledResources(report)
	for _, resource := range report.Resources {
		shown := slices.ContainsFunc(listed, func(r config.Resource) bool { return r.ID == resource.ID })
		if want := resource.Symbol() != config.GlyphOK; shown != want {
			t.Errorf("%s listed=%v, but Symbol says %q", resource.ID, shown, resource.Symbol())
		}
	}
}
