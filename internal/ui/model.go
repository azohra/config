package ui

import (
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	config "github.com/azohra/config/internal/config"
)

type screen int

const (
	screenDashboard screen = iota
	screenInventory
	screenPlan
	screenSnapshot
	screenRunning
)

type reportMsg struct {
	report config.Report
}

type Model struct {
	paths           config.Paths
	executable      string
	inspector       config.Inspector
	report          config.Report
	screen          screen
	afterInspect    screen
	loading         bool
	dashboardCursor int
	planCursor      int
	scroll          int
	choices         []planChoice
	input           textinput.Model
	spinner         spinner.Model
	width           int
	height          int
	operation       operation
	last            operationResult
}

func New(paths config.Paths, machine config.Machine, executable string) Model {
	input := textinput.New()
	input.Prompt = "Message  "
	input.Placeholder = "Describe this snapshot"
	input.CharLimit = 120
	input.SetWidth(60)
	spin := spinner.New(spinner.WithSpinner(spinner.Dot))
	spin.Style = accent
	return Model{
		paths:        paths,
		executable:   executable,
		inspector:    config.NewInspector(paths, machine, config.NewMachineRunner(paths)),
		screen:       screenDashboard,
		afterInspect: screenDashboard,
		loading:      true,
		input:        input,
		spinner:      spin,
		width:        80,
		height:       24,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.inspectCmd(), m.spinner.Tick)
}

func (m Model) inspectCmd() tea.Cmd {
	inspector := m.inspector
	return func() tea.Msg {
		return reportMsg{report: inspector.Inspect()}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var commands []tea.Cmd
	if m.loading || m.screen == screenRunning {
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		commands = append(commands, cmd)
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.SetWidth(min(70, max(8, m.width-14)))
		m.dashboardCursor = min(m.dashboardCursor, max(0, len(m.dashboardActions())-1))
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
				if m.report.Snapshot.Dirty > 0 {
					m.input.SetValue("")
					m.input.Err = nil
					commands = append(commands, m.input.Focus())
				}
			} else {
				m.screen = screenDashboard
			}
		default:
			m.screen = screenDashboard
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
		}
	default:
		if m.screen == screenSnapshot && m.report.Snapshot.Dirty > 0 {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			commands = append(commands, cmd)
		}
	}
	return m, tea.Batch(commands...)
}
