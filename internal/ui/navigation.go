package ui

import (
	"context"
	"slices"

	tea "charm.land/bubbletea/v2"

	config "github.com/azohra/config/internal/config"
)

type planChoice struct {
	resource config.Resource
	options  []config.Action
	choice   int
}

type dashboardAction int

const (
	dashboardReview dashboardAction = iota
	dashboardSave
	dashboardInspect
	dashboardUpdateSoftware
	dashboardUpdateRepositories
	dashboardLastResult
	dashboardCleanup
	dashboardQuit
)

func (p planChoice) action() config.Action {
	if len(p.options) == 0 {
		return config.Skip
	}
	return p.options[p.choice]
}

func (m Model) dashboardActions() []dashboardAction {
	var actions []dashboardAction
	if len(buildPlan(m.report)) > 0 {
		actions = append(actions, dashboardReview)
	}
	if m.report.Snapshot.NeedsSave() {
		actions = append(actions, dashboardSave)
	}
	actions = append(actions,
		dashboardInspect,
		dashboardUpdateSoftware,
		dashboardUpdateRepositories,
	)
	if m.last.label != "" {
		actions = append(actions, dashboardLastResult)
	}
	actions = append(actions, dashboardCleanup, dashboardQuit)
	return actions
}

// unsettledResources are the ones the dashboard lists. Resource.Symbol is
// internal/config's single answer for how a resource reads, so this asks it
// rather than defining a third rule beside it and Report.NeedsAttention.
func unsettledResources(report config.Report) []config.Resource {
	var resources []config.Resource
	for _, resource := range report.Resources {
		if resource.Symbol() != config.GlyphOK {
			resources = append(resources, resource)
		}
	}
	return resources
}

func (m Model) refreshInto(destination screen) (tea.Model, tea.Cmd) {
	m.cancelUpdatePlanning()
	m.afterInspect = destination
	m.loading = true
	return m, tea.Batch(m.inspectCmd(), m.spinner.Tick)
}

func (m Model) updateDashboard(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	actions := m.dashboardActions()
	switch key.String() {
	case "q", "esc":
		m.cancelUpdatePlanning()
		return m, tea.Quit
	case "up", "k":
		m.dashboardCursor = max(0, m.dashboardCursor-1)
	case "down", "j":
		m.dashboardCursor = min(max(0, len(actions)-1), m.dashboardCursor+1)
	case "enter":
		if m.dashboardCursor < 0 || m.dashboardCursor >= len(actions) {
			return m, nil
		}
		switch actions[m.dashboardCursor] {
		case dashboardReview:
			return m.refreshInto(screenPlan)
		case dashboardSave:
			return m.beginSnapshot()
		case dashboardInspect:
			m.scroll = 0
			m.screen = screenInventory
		case dashboardUpdateSoftware:
			return m.beginUpdate(config.UpdateSoftware)
		case dashboardUpdateRepositories:
			return m.beginUpdate(config.UpdateRepositories)
		case dashboardLastResult:
			m.scroll = 0
			m.screen = screenResult
		case dashboardCleanup:
			return m.beginPrune()
		case dashboardQuit:
			m.cancelUpdatePlanning()
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m Model) beginUpdate(scope config.UpdateScope) (tea.Model, tea.Cmd) {
	m.screen = screenUpdate
	m.updateScope = scope
	m.updatePreview = config.UpdatePlan{}
	m.updateError = nil
	m.checkingUpdate = true
	if m.overviewReady && m.overviewError == nil && m.updateOverview.Scope == scope {
		m.updatePreview = m.updateOverview
		m.checkingUpdate = false
		m.scroll = 0
		return m, nil
	}
	// The dashboard checks software only. Selecting that same scope promotes
	// the exact in-flight request; selecting repositories cancels it.
	if m.planCancel != nil && m.checkingOverview && m.planScope == scope {
		m.planPreview = true
		m.checkingOverview = false
		m.scroll = 0
		return m, m.spinner.Tick
	}
	m.scroll = 0
	cmd := m.startUpdatePlanning(scope, true)
	return m, tea.Batch(cmd, m.spinner.Tick)
}

func (m *Model) startUpdatePlanning(scope config.UpdateScope, preview bool) tea.Cmd {
	m.cancelUpdatePlanning()
	ctx, cancel := context.WithCancel(context.Background())
	m.planCancel = cancel
	m.planScope = scope
	m.planPreview = preview
	if preview {
		m.checkingUpdate = true
	} else {
		m.checkingOverview = true
	}
	return m.updatePlanCmd(ctx, m.planRequest, scope)
}

func (m Model) updateUpdate(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc", "q":
		m.cancelUpdatePlanning()
		m.screen = screenDashboard
		return m, nil
	case "r":
		if m.checkingUpdate {
			return m, nil
		}
		m.checkingUpdate = true
		m.updateError = nil
		return m, tea.Batch(m.startUpdatePlanning(m.updateScope, true), m.spinner.Tick)
	case "up", "k":
		m.scroll = max(0, m.scroll-1)
	case "down", "j":
		m.scroll = min(m.scroll+1, m.scrollBound())
	case "pgup":
		m.scroll = max(0, m.scroll-10)
	case "pgdown":
		m.scroll = min(m.scroll+10, m.scrollBound())
	case "enter":
		if m.checkingUpdate {
			return m, nil
		}
		if m.updateError != nil || !m.updatePreview.HasWork() {
			m.screen = screenDashboard
			return m, nil
		}
		label, scope := "Software update", "software"
		if m.updateScope == config.UpdateRepositories {
			label, scope = "Repository update", "repositories"
		}
		return m.startOperation(label, m.executable, "--run-update", scope, m.updatePreview.Fingerprint())
	}
	return m, nil
}

func (m Model) updateResult(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc", "q", "enter":
		m.screen = screenDashboard
	case "up", "k":
		m.scroll = max(0, m.scroll-1)
	case "down", "j":
		m.scroll = min(m.scroll+1, m.scrollBound())
	case "pgup":
		m.scroll = max(0, m.scroll-10)
	case "pgdown":
		m.scroll = min(m.scroll+10, m.scrollBound())
	}
	return m, nil
}

func (m Model) updateInventory(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc", "q":
		m.screen = screenDashboard
	case "up", "k":
		m.scroll = max(0, m.scroll-1)
	case "down", "j":
		m.scroll = min(m.scroll+1, m.scrollBound())
	case "pgup":
		m.scroll = max(0, m.scroll-10)
	case "pgdown":
		m.scroll = min(m.scroll+10, m.scrollBound())
	}
	return m, nil
}

func buildPlan(report config.Report) []planChoice {
	var choices []planChoice
	for _, resource := range report.Resources {
		if len(resource.Actions) == 0 {
			continue
		}
		options := slices.Clone(resource.Actions)
		if resource.Bidirectional {
			options = append([]config.Action{config.Skip}, options...)
		} else {
			options = append(options, config.Skip)
		}
		choices = append(choices, planChoice{resource: resource, options: options})
	}
	return choices
}

func planSelections(choices []planChoice) []config.Selection {
	var selections []config.Selection
	for _, choice := range choices {
		if choice.action() != config.Skip {
			selections = append(selections, config.Selection{ID: choice.resource.ID, Action: choice.action()})
		}
	}
	return selections
}

func (m Model) planItemCount() int {
	count := len(m.choices)
	if len(planSelections(m.choices)) > 0 {
		count++
	}
	return count
}

func (m *Model) cycleChoice(delta int) {
	if m.planCursor < 0 || m.planCursor >= len(m.choices) {
		return
	}
	choice := &m.choices[m.planCursor]
	choice.choice = (choice.choice + delta + len(choice.options)) % len(choice.options)
}

func (m Model) updatePlan(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.screen = screenDashboard
	case "up", "k":
		m.planCursor = max(0, m.planCursor-1)
	case "down", "j":
		m.planCursor = min(max(0, m.planItemCount()-1), m.planCursor+1)
	case "left", "h":
		m.cycleChoice(-1)
	case "right", "l", " ":
		m.cycleChoice(1)
	case "enter":
		if m.planCursor < len(m.choices) {
			m.planCursor = min(max(0, m.planItemCount()-1), m.planCursor+1)
			return m, nil
		}
		selections := planSelections(m.choices)
		if len(selections) == 0 {
			return m, nil
		}
		encoded, err := config.EncodeSelections(selections)
		if err != nil {
			m.last = operationResult{label: "Apply", err: err}
			m.screen = screenDashboard
			return m, nil
		}
		return m.startOperation("Apply", m.executable, "--apply", encoded)
	}
	return m, nil
}

func (m Model) beginSnapshot() (tea.Model, tea.Cmd) {
	m.scroll = 0
	return m.refreshInto(screenSnapshot)
}

func (m Model) updateSnapshot(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.screen = screenDashboard
		return m, nil
	case "pgup":
		m.scroll = max(0, m.scroll-10)
		return m, nil
	case "pgdown":
		m.scroll = min(m.scroll+10, m.scrollBound())
		return m, nil
	case "enter":
		return m.startOperation("Save", m.executable, "--snapshot")
	}
	return m, nil
}

func (m Model) beginPrune() (tea.Model, tea.Cmd) {
	m.cancelUpdatePlanning()
	m.screen = screenPrune
	m.loading = true
	m.scroll = 0
	m.prunePreview = ""
	m.pruneHasWork = false
	return m, tea.Batch(m.pruneCmd(), m.spinner.Tick)
}

func (m Model) updatePrune(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc", "q":
		m.screen = screenDashboard
	case "up", "k":
		m.scroll = max(0, m.scroll-1)
	case "down", "j":
		m.scroll = min(m.scroll+1, m.scrollBound())
	case "pgup":
		m.scroll = max(0, m.scroll-10)
	case "pgdown":
		m.scroll = min(m.scroll+10, m.scrollBound())
	case "enter":
		if m.pruneHasWork {
			return m.startOperation("Cleanup", m.executable, "prune", "--yes")
		}
		m.screen = screenDashboard
	}
	return m, nil
}
