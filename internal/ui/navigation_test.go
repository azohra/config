package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	config "github.com/azohra/config/internal/config"
)

func press(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code})
}

func TestBuildPlanUsesInspectedActionsAndSafeDefaults(t *testing.T) {
	report := config.Report{Resources: []config.Resource{
		{ID: "setup", Name: "Machine setup", State: config.Drift, Actions: []config.Action{config.Apply}},
		{ID: "example-app", Name: "Example App", State: config.LiveChanged, Bidirectional: true, Actions: []config.Action{config.Capture, config.Apply}},
	}}
	plan := buildPlan(report)
	if len(plan) != 2 {
		t.Fatalf("plan has %d choices, want 2", len(plan))
	}
	if plan[0].action() != config.Apply {
		t.Fatalf("authoritative action = %q, want apply", plan[0].action())
	}
	if plan[1].action() != config.Skip || plan[1].options[1] != config.Capture {
		t.Fatalf("bidirectional options = %v, want skip then capture", plan[1].options)
	}
}

func TestReviewRefreshesBeforeBuildingPlan(t *testing.T) {
	report := config.Report{Resources: []config.Resource{{
		ID: "setup", Name: "Machine setup", State: config.Drift, Actions: []config.Action{config.Apply},
	}}}
	m := Model{screen: screenDashboard, report: report}
	next, _ := m.updateDashboard(press(tea.KeyEnter))
	refreshing := next.(Model)
	if !refreshing.loading || refreshing.afterInspect != screenPlan {
		t.Fatalf("loading=%v after=%v, want loading plan", refreshing.loading, refreshing.afterInspect)
	}
	next, _ = refreshing.Update(reportMsg{report: report})
	got := next.(Model)
	if got.loading || got.screen != screenPlan || len(got.choices) != 1 {
		t.Fatalf("loading=%v screen=%v choices=%d", got.loading, got.screen, len(got.choices))
	}
}

func TestPlanConfirmsInlineChoiceAndRunsExplicitSelection(t *testing.T) {
	m := Model{
		screen: screenPlan,
		choices: []planChoice{{
			resource: config.Resource{ID: "dock", Bidirectional: true},
			options:  []config.Action{config.Skip, config.Capture, config.Apply},
		}},
		executable: "/bin/true",
	}
	next, _ := m.updatePlan(press(tea.KeyRight))
	m = next.(Model)
	next, _ = m.updatePlan(press(tea.KeyEnter))
	got := next.(Model)
	if got.screen != screenPlan || got.choices[0].action() != config.Capture || got.planCursor != 1 {
		t.Fatalf("screen=%v action=%q cursor=%d, want capture confirmed onto run", got.screen, got.choices[0].action(), got.planCursor)
	}
	next, _ = got.updatePlan(press(tea.KeyEnter))
	if running := next.(Model); running.screen != screenRunning {
		t.Fatalf("run opened screen %v", running.screen)
	}
}

func TestInspectOpensCombinedEvidenceView(t *testing.T) {
	report := config.Report{
		Resources: []config.Resource{{ID: "setup", Name: "Machine setup", State: config.Current}},
		Snapshot:  config.SnapshotStatus{Upstream: "origin/main"},
	}
	m := Model{screen: screenDashboard, report: report}
	next, cmd := m.updateDashboard(press(tea.KeyEnter))
	if got := next.(Model); got.screen != screenInventory {
		t.Fatalf("screen=%v, want inventory", got.screen)
	}
	if cmd != nil {
		t.Fatal("inspect started an unnecessary refresh")
	}
}

func TestUpdateRunsOnlyWhenSelected(t *testing.T) {
	m := Model{
		screen:          screenDashboard,
		report:          config.Report{Snapshot: config.SnapshotStatus{Upstream: "origin/main"}},
		dashboardCursor: 1,
		executable:      "/tmp/config",
	}
	next, cmd := m.updateDashboard(press(tea.KeyEnter))
	running := next.(Model)
	if running.screen != screenRunning || running.operation.label != "Update" {
		t.Fatalf("screen=%v label=%q, want running Update", running.screen, running.operation.label)
	}
	if cmd == nil {
		t.Fatal("Update did not start an operation")
	}
}

func TestSnapshotRefreshesShowsFilesAndRequiresMessage(t *testing.T) {
	report := config.Report{Snapshot: config.SnapshotStatus{
		Dirty: 2, Changes: []string{" M README.md", "?? cmd/config/main.go"},
		Upstream: "origin/main", Destination: "origin/main",
	}}
	m := New(config.Paths{}, config.Machine{}, "/tmp/config")
	m.screen, m.report, m.width, m.height = screenDashboard, report, 80, 24
	next, _ := m.beginSnapshot()
	refreshing := next.(Model)
	if !refreshing.loading || refreshing.afterInspect != screenSnapshot {
		t.Fatalf("loading=%v after=%v", refreshing.loading, refreshing.afterInspect)
	}
	next, _ = refreshing.Update(reportMsg{report: report})
	got := next.(Model)
	view := got.renderSnapshot()
	for _, want := range []string{"2 changed files", "README.md", "cmd/config/main.go", "Message"} {
		if !strings.Contains(view, want) {
			t.Fatalf("snapshot missing %q:\n%s", want, view)
		}
	}
	next, _ = got.updateSnapshot(press(tea.KeyEnter))
	if empty := next.(Model); empty.input.Err == nil || empty.screen != screenSnapshot {
		t.Fatal("empty snapshot message was accepted")
	}
}

func TestResizeClampsDashboardAction(t *testing.T) {
	m := Model{
		report:          config.Report{Snapshot: config.SnapshotStatus{Upstream: "origin/main"}},
		dashboardCursor: 99,
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 16})
	got := next.(Model)
	if got.dashboardCursor != len(got.dashboardActions())-1 {
		t.Fatalf("cursor=%d actions=%d", got.dashboardCursor, len(got.dashboardActions()))
	}
}
