package ui

import (
	"bytes"
	"context"
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
	report  config.Report
	passive bool
}

type prunePlanMsg struct {
	request     uint64
	preview     string
	hasWork     bool
	fingerprint string
	err         error
}

type updatePlanMsg struct {
	request uint64
	plan    config.UpdatePlan
	err     error
}

type Model struct {
	paths            config.Paths
	executable       string
	version          string
	restart          bool
	reopenResult     bool
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
	pruneFingerprint string
	pruneRequest     uint64
	updateOverview   config.UpdatePlan
	overviewReady    bool
	overviewError    error
	updatePreview    config.UpdatePlan
	updateScope      config.UpdateScope
	planScope        config.UpdateScope
	checkingOverview bool
	checkingUpdate   bool
	updateError      error
	planRequest      uint64
	planPreview      bool
	planContext      context.Context
	planCancel       context.CancelFunc
	spinner          spinner.Model
	width            int
	height           int
	showDiagnostics  bool
	operation        operation
	last             operationResult
}

func New(paths config.Paths, machine config.Machine, executable, version string, reopen ...bool) Model {
	spin := spinner.New(spinner.WithSpinner(spinner.Dot))
	spin.Style = accent
	ctx, cancel := context.WithCancel(context.Background())
	m := Model{
		paths:            paths,
		executable:       executable,
		version:          version,
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
		planRequest:      1,
		planContext:      ctx,
		planCancel:       cancel,
		last:             loadOperationResult(paths),
	}
	if len(reopen) > 0 && reopen[0] && m.last.label != "" {
		m.planCancel()
		m.planCancel = nil
		m.planRequest++
		m.checkingOverview = false
		m.loading = false
		m.screen = screenResult
		m.showDiagnostics = m.last.err != nil && m.last.log.hasDiagnostics()
		if m.showDiagnostics {
			m.scroll = m.scrollBound()
		}
		m.reopenResult = true
	}
	return m
}

func (m Model) Init() tea.Cmd {
	if m.reopenResult {
		return m.inspectCmd(true)
	}
	return tea.Batch(m.inspectCmd(), m.updatePlanCmd(m.planContext, m.planRequest), m.spinner.Tick)
}

func (m Model) updatePlanCmd(ctx context.Context, request uint64) tea.Cmd {
	planner := m.updater
	scope := m.planScope
	return func() tea.Msg {
		plan, err := planner.PlanContext(ctx, scope)
		return updatePlanMsg{request: request, plan: plan, err: err}
	}
}

func (m Model) inspectCmd(passive ...bool) tea.Cmd {
	inspector := m.inspector
	return func() tea.Msg {
		return reportMsg{report: inspector.Inspect(), passive: len(passive) > 0 && passive[0]}
	}
}

func (m Model) pruneCmd() tea.Cmd {
	planner := m.pruner
	request := m.pruneRequest
	return func() tea.Msg {
		plan, err := planner.Plan()
		if err != nil {
			return prunePlanMsg{request: request, err: err}
		}
		var preview bytes.Buffer
		config.WritePrunePlan(&preview, plan)
		return prunePlanMsg{
			request:     request,
			preview:     strings.TrimSpace(preview.String()),
			hasWork:     !plan.Empty(),
			fingerprint: plan.Fingerprint(),
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
		followedTail := m.screen == screenResult && m.showDiagnostics && m.scroll == m.scrollBound()
		m.width, m.height = msg.Width, msg.Height
		if followedTail {
			m.scroll = m.scrollBound()
		}
	case reportMsg:
		m.report = msg.report
		if msg.passive {
			break
		}
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
		default:
			m.screen = screenDashboard
		}
	case prunePlanMsg:
		if m.screen != screenPrune || msg.request != m.pruneRequest {
			break
		}
		m.loading = false
		m.scroll = 0
		if msg.err != nil {
			m.last = operationResult{label: "Cleanup", err: msg.err}
			m.screen = screenDashboard
			break
		}
		m.prunePreview = msg.preview
		m.pruneHasWork = msg.hasWork
		m.pruneFingerprint = msg.fingerprint
		m.screen = screenPrune
	case updatePlanMsg:
		if msg.request != m.planRequest {
			break
		}
		if m.planCancel != nil {
			m.planCancel()
			m.planCancel = nil
		}
		if m.planPreview {
			if m.screen != screenUpdate {
				break
			}
			m.checkingUpdate = false
			m.updateError = msg.err
			m.updatePreview = msg.plan
			if m.updateScope == config.UpdateConfig {
				m.overviewReady = msg.err == nil
				m.overviewError = msg.err
				m.updateOverview = msg.plan
			}
			m.scroll = 0
			m.screen = screenUpdate
		} else {
			m.overviewReady = msg.err == nil
			m.overviewError = msg.err
			m.updateOverview = msg.plan
			m.checkingOverview = false
		}
	case operationEventsMsg:
		return m.updateOperation(msg)
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			if m.screen == screenRunning {
				m.cancelOperation()
				return m, tea.Batch(commands...)
			}
			m.cancelUpdatePlanning()
			return m, tea.Quit
		}
		if m.screen == screenRunning && msg.String() == "d" {
			m.showDiagnostics = !m.showDiagnostics
			m.scroll = 0
			return m, tea.Batch(commands...)
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

func (m *Model) cancelUpdatePlanning() {
	if m.planCancel != nil {
		m.planCancel()
		m.planCancel = nil
	}
	m.planRequest++
	m.checkingOverview = false
	m.checkingUpdate = false
}

func (m Model) RestartRequested() bool { return m.restart }
