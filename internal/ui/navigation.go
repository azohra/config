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
	dashboardUpdateConfig
	dashboardUpdateSoftware
	dashboardUpdateRepositories
	dashboardRepair
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
		dashboardUpdateConfig,
		dashboardUpdateSoftware,
		dashboardUpdateRepositories,
		dashboardRepair,
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
		case dashboardUpdateConfig:
			return m.beginUpdate()
		case dashboardUpdateSoftware:
			return m.beginUpdate(config.UpdateSoftware)
		case dashboardUpdateRepositories:
			return m.beginUpdate(config.UpdateRepositories)
		case dashboardRepair:
			return m.beginUpdate(config.UpdateRepair)
		case dashboardLastResult:
			m.showDiagnostics = m.last.err != nil && m.last.log.hasDiagnostics()
			m.screen = screenResult
			m.scroll = 0
			if m.showDiagnostics {
				m.scroll = m.scrollBound()
			}
		case dashboardCleanup:
			return m.beginPrune()
		case dashboardQuit:
			m.cancelUpdatePlanning()
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m Model) beginUpdate(selected ...config.UpdateScope) (tea.Model, tea.Cmd) {
	scope := config.UpdateConfig
	if len(selected) > 0 {
		scope = selected[0]
	}
	m.updateScope = scope
	m.showCommands, m.authorizationError = false, nil
	m.screen = screenUpdate
	m.updatePreview = config.UpdatePlan{}
	m.updateCursor, m.updateSkipped = 0, nil
	m.updateError = nil
	m.checkingUpdate = true
	if m.overviewReady && m.overviewError == nil && m.updateOverview.Scope == scope {
		m.updatePreview = m.updateOverview
		m.checkingUpdate = false
		m.scroll = 0
		return m, nil
	}
	// Opening the preview reuses the dashboard's in-flight release check.
	if m.planCancel != nil && m.checkingOverview && m.planScope == scope {
		m.planPreview = true
		m.checkingOverview = false
		m.scroll = 0
		return m, m.spinner.Tick
	}
	m.scroll = 0
	cmd := m.startUpdatePlanning(true)
	return m, tea.Batch(cmd, m.spinner.Tick)
}

func (m *Model) startUpdatePlanning(preview bool) tea.Cmd {
	m.cancelUpdatePlanning()
	ctx, cancel := context.WithCancel(context.Background())
	m.planCancel = cancel
	m.planPreview = preview
	m.planScope = m.updateScope
	if preview {
		m.checkingUpdate = true
	} else {
		m.checkingOverview = true
	}
	return m.updatePlanCmd(ctx, m.planRequest)
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
		return m, tea.Batch(m.startUpdatePlanning(true), m.spinner.Tick)
	case "up", "k":
		m.moveUpdateCursor(-1)
	case "down", "j":
		m.moveUpdateCursor(1)
	case "pgup":
		m.scroll = max(0, m.scroll-10)
	case "pgdown":
		m.scroll = min(m.scroll+10, m.scrollBound())
	case "space", " ":
		if keys := m.updateKeys(); !m.showCommands && len(keys) > 1 && m.updateCursor < len(keys) {
			m.toggleUpdateItems(keys[m.updateCursor : m.updateCursor+1])
		}
	case "a":
		if keys := m.updateKeys(); !m.showCommands && len(keys) > 1 {
			m.toggleUpdateItems(keys)
		}
	case "d":
		if m.updateScope != config.UpdateConfig && !m.checkingUpdate {
			m.showCommands = !m.showCommands
			m.scroll = 0
		}
	case "enter":
		if m.checkingUpdate || m.authorizing {
			return m, nil
		}
		if m.updateError != nil || !m.updatePreview.HasWork() {
			m.screen = screenDashboard
			return m, nil
		}
		plan, keys, err := m.selectedUpdate()
		if err != nil || !plan.HasWork() {
			return m, nil
		}
		label := m.updateScope.Label()
		if m.updateScope == config.UpdateConfig {
			label = "Config update"
		}
		args := append([]string{"--run-update", m.updateScope.String(), plan.Fingerprint()}, keys...)
		if plan.Elevated() {
			return m.authorizeThen(label, args...)
		}
		return m.startOperation(label, m.executable, args...)
	}
	return m, nil
}

// updateRow is one line of an update review. A row with a key is an item the
// person can leave out of the run; a note is a step's remark.
type updateRow struct {
	text string
	key  string
	note bool
}

func (m Model) updateRows() []updateRow {
	var rows []updateRow
	for _, group := range m.updatePreview.Groups {
		summary := updateRow{text: group.Summary}
		// A step that is its own single item is selected on its summary line.
		own := len(group.Items) == 1 && group.Items[0].Name == group.Name
		if own {
			summary.key = group.Key(group.Items[0])
		}
		rows = append(rows, summary)
		for _, item := range group.Items {
			if own {
				break
			}
			rows = append(rows, updateRow{text: "  " + item.Label(), key: group.Key(item)})
		}
		for _, detail := range group.Details {
			rows = append(rows, updateRow{text: "  " + detail, note: true})
		}
	}
	return rows
}

func (m Model) updateKeys() []string {
	var keys []string
	for _, row := range m.updateRows() {
		if row.key != "" {
			keys = append(keys, row.key)
		}
	}
	return keys
}

// moveUpdateCursor moves between items and keeps the focused one on screen.
// A review with nothing to select scrolls instead.
func (m *Model) moveUpdateCursor(delta int) {
	keys := m.updateKeys()
	if m.showCommands || len(keys) == 0 {
		m.scroll = min(max(0, m.scroll+delta), m.scrollBound())
		return
	}
	m.updateCursor = min(max(0, m.updateCursor+delta), len(keys)-1)
	line := 1 // the scope heading
	for _, row := range m.updateRows() {
		if row.key == keys[m.updateCursor] {
			break
		}
		line++
	}
	available := max(5, m.height-updateChrome)
	m.scroll = min(max(m.scroll, line-available+1), line)
	if m.updateCursor == 0 {
		m.scroll = 0
	}
}

// toggleUpdateItems leaves the items out when any of them is selected, and
// selects them all otherwise.
func (m *Model) toggleUpdateItems(keys []string) {
	if m.updateSkipped == nil {
		m.updateSkipped = make(map[string]bool)
	}
	skip := slices.ContainsFunc(keys, func(key string) bool { return !m.updateSkipped[key] })
	for _, key := range keys {
		m.updateSkipped[key] = skip
	}
}

// selectedUpdate is the plan the person confirmed and the keys that select it.
// Leaving nothing out runs every step whole.
func (m Model) selectedUpdate() (config.UpdatePlan, []string, error) {
	var keys []string
	skipped := false
	for _, key := range m.updateKeys() {
		if m.updateSkipped[key] {
			skipped = true
		} else {
			keys = append(keys, key)
		}
	}
	if !skipped {
		return m.updatePreview, nil, nil
	}
	if len(keys) == 0 {
		return config.UpdatePlan{}, nil, nil
	}
	// A step with work but no items of its own runs alongside any selection.
	for _, group := range m.updatePreview.Groups {
		if len(group.Items) == 0 && (group.State == config.UpdateAvailable || group.State == config.UpdatePending) {
			keys = append(keys, group.ID)
		}
	}
	plan, err := m.updatePreview.Select(keys)
	return plan, keys, err
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
	case "d":
		m.showDiagnostics = !m.showDiagnostics
		m.scroll = 0
		if m.showDiagnostics && m.last.err != nil {
			m.scroll = m.scrollBound()
		}
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
	m.pruneRequest++
	m.screen = screenPrune
	m.loading = true
	m.scroll = 0
	m.prunePreview, m.pruneCommands = "", ""
	m.pruneHasWork, m.pruneElevated = false, false
	m.pruneFingerprint = ""
	m.showCommands, m.authorizationError = false, nil
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
	case "d":
		m.showCommands = !m.showCommands
		m.scroll = 0
	case "enter":
		if m.authorizing {
			return m, nil
		}
		if m.pruneHasWork && m.pruneElevated {
			return m.authorizeThen("Cleanup", "--run-prune", m.pruneFingerprint)
		}
		if m.pruneHasWork {
			return m.startOperation("Cleanup", m.executable, "--run-prune", m.pruneFingerprint)
		}
		m.screen = screenDashboard
	}
	return m, nil
}
