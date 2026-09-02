package ui

import (
	"bytes"
	"io"
	"strings"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	config "github.com/azohra/config/internal/config"
)

type screen int

const (
	screenDashboard screen = iota
	screenInventory
	screenPlan
	screenSnapshot
	screenPrune
	screenUpdate
	screenRunning
	screenResult
)

type reportMsg struct {
	report config.Report
}

type prunePlanMsg struct {
	preview string
	hasWork bool
	err     error
}

type updatePlanMsg struct {
	plan    config.UpdatePlan
	scope   config.UpdateScope
	preview bool
	err     error
}

type Model struct {
	paths            config.Paths
	executable       string
	inspector        config.Inspector
	pruner           config.Pruner
	updater          config.Updater
	report           config.Report
	screen           screen
	afterInspect     screen
	loading          bool
	dashboardCursor  int
	planCursor       int
	scroll           int
	choices          []planChoice
	prunePreview     string
	pruneHasWork     bool
	updateOverview   config.UpdatePlan
	overviewReady    bool
	overviewError    error
	updatePreview    config.UpdatePlan
	updateScope      config.UpdateScope
	checkingOverview bool
	checkingUpdate   bool
	awaitingUpdate   bool
	updateError      error
	spinner          spinner.Model
	width            int
	height           int
	operation        operation
	last             operationResult
}

func New(paths config.Paths, machine config.Machine, executable, version string) Model {
	spin := spinner.New(spinner.WithSpinner(spinner.Dot))
	spin.Style = accent
	return Model{
		paths:            paths,
		executable:       executable,
		inspector:        config.NewInspector(paths, machine, config.NewMachineRunner(paths)),
		pruner:           config.NewPruner(paths, machine, io.Discard),
		updater:          config.NewUpdater(paths, io.Discard, version),
		screen:           screenDashboard,
		afterInspect:     screenDashboard,
		loading:          true,
		spinner:          spin,
		width:            80,
		height:           24,
		checkingOverview: true,
		last:             loadOperationResult(paths),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.inspectCmd(), m.updatePlanCmd(config.UpdateAll, false), m.spinner.Tick)
}

func (m Model) updatePlanCmd(scope config.UpdateScope, preview bool) tea.Cmd {
	planner := m.updater
	return func() tea.Msg {
		plan, err := planner.Plan(scope)
		return updatePlanMsg{plan: plan, scope: scope, preview: preview, err: err}
	}
}

func (m Model) inspectCmd() tea.Cmd {
	inspector := m.inspector
	return func() tea.Msg {
		return reportMsg{report: inspector.Inspect()}
	}
}

func (m Model) pruneCmd() tea.Cmd {
	planner := m.pruner
	return func() tea.Msg {
		plan, err := planner.Plan()
		if err != nil {
			return prunePlanMsg{err: err}
		}
		var preview bytes.Buffer
		config.WritePrunePlan(&preview, plan)
		return prunePlanMsg{
			preview: strings.TrimSpace(preview.String()),
			hasWork: !plan.Empty(),
		}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var commands []tea.Cmd
	if m.loading || m.checkingOverview || m.checkingUpdate || m.screen == screenRunning {
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		commands = append(commands, cmd)
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case reportMsg:
		m.report = msg.report
		m.loading = false
		m.scroll = 0
		switch m.afterInspect {
		case screenPlan:
			m.choices = buildPlan(m.report)
			m.planCursor = 0
			if len(m.choices) == 0 {
				m.screen = screenDashboard
			} else {
				m.screen = screenPlan
			}
		case screenSnapshot:
			if m.report.Snapshot.NeedsSave() {
				m.screen = screenSnapshot
			} else {
				m.screen = screenDashboard
			}
		case screenResult:
			m.screen = screenResult
		default:
			m.screen = screenDashboard
		}
	case prunePlanMsg:
		m.loading = false
		m.scroll = 0
		if msg.err != nil {
			m.last = operationResult{label: "Cleanup", err: msg.err}
			m.screen = screenDashboard
			break
		}
		m.prunePreview = msg.preview
		m.pruneHasWork = msg.hasWork
		m.screen = screenPrune
	case updatePlanMsg:
		if msg.preview {
			if !m.awaitingUpdate || m.screen != screenUpdate || msg.scope != m.updateScope {
				break
			}
			m.checkingUpdate = false
			m.awaitingUpdate = false
			m.updateError = msg.err
			m.updatePreview = msg.plan
			m.updateScope = msg.scope
			m.scroll = 0
			m.screen = screenUpdate
		} else {
			m.overviewReady = msg.err == nil
			m.overviewError = msg.err
			m.updateOverview = msg.plan
			m.checkingOverview = false
			if m.screen == screenUpdate && m.checkingUpdate && m.awaitingUpdate {
				m.checkingUpdate = false
				m.updateError = msg.err
				m.updatePreview = msg.plan
				m.updatePreview.Scope = m.updateScope
				m.scroll = 0
				m.awaitingUpdate = false
			}
		}
	case operationEventMsg:
		return m.updateOperation(msg)
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			if m.screen == screenRunning {
				m.cancelOperation()
				return m, tea.Batch(commands...)
			}
			return m, tea.Quit
		}
		if m.loading || m.screen == screenRunning {
			return m, tea.Batch(commands...)
		}
		switch m.screen {
		case screenDashboard:
			return m.updateDashboard(msg)
		case screenInventory:
			return m.updateInventory(msg)
		case screenPlan:
			return m.updatePlan(msg)
		case screenSnapshot:
			return m.updateSnapshot(msg)
		case screenPrune:
			return m.updatePrune(msg)
		case screenUpdate:
			return m.updateUpdate(msg)
		case screenResult:
			return m.updateResult(msg)
		}
	}
	// The action list is rebuilt from every report, so clamping only on a
	// resize left the highlighted row pointing at a different action, or at
	// none, after an operation.
	m.dashboardCursor = min(max(0, m.dashboardCursor), max(0, len(m.dashboardActions())-1))
	m.scroll = min(max(0, m.scroll), m.scrollBound())
	return m, tea.Batch(commands...)
}
