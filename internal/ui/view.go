package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	config "github.com/azohra/config/internal/config"
)

// Each screen's chrome is its fixed line count around the scrollable content:
// frame padding, title, headers, and key hints. Content budgets subtract it
// from the terminal height, so a chrome value must change with its screen's
// block structure.
const (
	inspectingChrome = 12
	dashboardChrome  = 14 // plus one line per dashboard action
	inventoryChrome  = 9
	planChrome       = 14 // plus one line per plan row
	snapshotChrome   = 11
	pruneChrome      = 13
	runningChrome    = 10
)

func (m Model) View() tea.View {
	view := tea.NewView(m.render())
	view.AltScreen = true
	view.WindowTitle = "Config"
	return view
}

func (m Model) render() string {
	if m.loading {
		if m.screen == screenPrune {
			return m.renderPrunePlanning()
		}
		return m.renderInspecting()
	}
	switch m.screen {
	case screenInventory:
		return m.renderInventory()
	case screenPlan:
		return m.renderPlan()
	case screenSnapshot:
		return m.renderSnapshot()
	case screenPrune:
		return m.renderPrune()
	case screenRunning:
		return m.renderRunning()
	default:
		return m.renderDashboard()
	}
}

func (m Model) renderInspecting() string {
	blocks := []string{title.Render("CONFIG")}
	if m.last.label != "" {
		if output := m.operationTail(m.last.output, max(4, m.height-inspectingChrome)); output != "" {
			blocks = append(blocks, output)
		}
		blocks = append(blocks, m.spinner.View()+" Refreshing status…")
	} else {
		blocks = append(blocks, m.spinner.View()+" Inspecting this Mac…")
	}
	return frame(m.width, blocks...)
}

func (m Model) renderDashboard() string {
	header := fmt.Sprintf("%s · %s", m.report.Snapshot.Branch, m.report.Snapshot.Commit)
	if m.report.Snapshot.Dirty > 0 {
		header += " · changed"
	} else if m.report.Snapshot.Ahead > 0 {
		header += " · unpushed"
	} else {
		header += " · clean"
	}
	headline, detail := dashboardHealth(m.report)
	status := headline + "\n" + muted.Render(detail)
	resources, hidden := m.dashboardUnsettled()
	if len(resources) > 0 {
		var attention []string
		for _, resource := range resources {
			attention = append(attention, "  "+resourceRow(resource))
		}
		if hidden > 0 {
			attention = append(attention, muted.Render("  … "+config.FormatCount(hidden, "more resource", "more resources")))
		}
		status += "\n\n" + muted.Render("Not current") + "\n" + strings.Join(attention, "\n")
	}

	var actionLines []string
	for index, action := range m.dashboardActions() {
		label, actionDetail := m.dashboardActionText(action)
		line := fmt.Sprintf("%-22s %s", label, muted.Render(actionDetail))
		actionLines = append(actionLines, focusRow(index == m.dashboardCursor, line))
	}
	blocks := []string{title.Render("CONFIG") + "  " + muted.Render(header), status}
	if result := m.resultBanner(); result != "" {
		blocks = append(blocks, result)
	}
	blocks = append(blocks, muted.Render("Actions")+"\n"+strings.Join(actionLines, "\n"), keyHints("↑/↓ move", "enter select", "q quit"))
	return frame(m.width, blocks...)
}

func (m Model) dashboardUnsettled() ([]config.Resource, int) {
	resources := unsettledResources(m.report)
	height := m.height
	if height <= 0 {
		height = 24
	}
	budget := max(0, height-dashboardChrome-len(m.dashboardActions()))
	if len(resources) <= budget {
		return resources, 0
	}
	visible := max(0, budget-1)
	return resources[:visible], len(resources) - visible
}

func (m Model) resultBanner() string {
	if m.last.label == "" {
		return ""
	}
	symbol := good.Render("✓")
	message := m.last.label + " complete"
	if m.last.cancelled {
		symbol = caution.Render("!")
		message = m.last.label + " cancelled"
	} else if m.last.err != nil {
		symbol = bad.Render("✗")
		message = m.last.label + " failed"
		if output := m.operationTail(m.last.output, 3); output != "" {
			message += "\n" + output
		} else {
			message += " — " + m.last.err.Error()
		}
	}
	return symbol + " " + message
}

func dashboardHealth(report config.Report) (string, string) {
	failures, decisions, advisories := report.Counts()
	plan := buildPlan(report)
	headline := good.Render("✓ Configuration matches")
	switch {
	case failures > 0:
		headline = bad.Render("✗ Config needs attention")
	case decisions > 0:
		headline = caution.Render("↔ Your choice is needed")
	case len(plan) > 0:
		headline = caution.Render("! Changes are ready to review")
	case report.Snapshot.NeedsSave():
		headline = caution.Render("! Changes are ready to save")
	case advisories+report.Snapshot.Warnings() > 0:
		headline = caution.Render("! Config has advisories")
	}

	var parts []string
	if failures > 0 {
		parts = append(parts, config.FormatCount(failures, "failed check", "failed checks"))
	}
	if decisions > 0 {
		parts = append(parts, config.FormatCount(decisions, "choice required", "choices required"))
	}
	authoritative := 0
	for _, choice := range plan {
		if !choice.resource.Bidirectional {
			authoritative++
		}
	}
	if authoritative > 0 {
		parts = append(parts, config.FormatCount(authoritative, "proposed fix", "proposed fixes"))
	}
	parts = append(parts, report.Snapshot.PendingParts()...)
	if advisories > 0 {
		parts = append(parts, config.FormatCount(advisories, "advisory", "advisories"))
	}
	if len(parts) == 0 {
		destination := report.Snapshot.Destination
		if destination == "" {
			destination = report.Snapshot.Upstream
		}
		return headline, "Machine state and " + destination + " agree; cleanup runs on demand."
	}
	return headline, strings.Join(parts, " · ")
}

func focusRow(focused bool, line string) string {
	if focused {
		return cursorStyle.Render("▶") + " " + selected.Render(line)
	}
	return "  " + line
}

func (m Model) dashboardActionText(action dashboardAction) (string, string) {
	switch action {
	case dashboardReview:
		return "Review changes", config.FormatCount(len(buildPlan(m.report)), "item", "items")
	case dashboardSave:
		destination := m.report.Snapshot.Destination
		if destination == "" {
			destination = config.FormatCount(m.report.Snapshot.Dirty+m.report.Snapshot.Ahead, "pending item", "pending items")
		}
		if m.report.Snapshot.Dirty > 0 {
			return "Save snapshot", "→ " + destination
		}
		return "Publish snapshot", "→ " + destination
	case dashboardInspect:
		return "Inspect configuration", config.FormatCount(len(m.report.Resources), "resource", "resources")
	case dashboardUpdateSoftware:
		return "Update software", "Config, mise, tools, and packages"
	case dashboardUpdateRepositories:
		return "Update repositories", "fetch and fast-forward clean checkouts"
	case dashboardCleanup:
		return "Clean up", "preview unused tools and Config state"
	default:
		return "Quit", ""
	}
}

func inventoryLines(resources []config.Resource) []string {
	var lines []string
	for index, resource := range resources {
		if index > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, resourceRow(resource))
		for _, check := range resource.Checks {
			line := "    " + checkSymbol(check) + " " + check.Label
			if check.Detail != "" {
				line += muted.Render(" — " + check.Detail)
			}
			lines = append(lines, line)
		}
		for _, detail := range resource.Details {
			lines = append(lines, "    "+accent.Render("→")+" "+detail)
		}
	}
	return lines
}

func (m Model) renderInventory() string {
	lines := inventoryLines(m.report.Resources)
	if len(lines) == 0 {
		lines = []string{muted.Render("No configuration resources found.")}
	}
	available := max(5, m.height-inventoryChrome)
	scrollable := len(lines) > available
	lines = visibleLines(lines, m.scroll, available)
	hints := []string{"esc dashboard"}
	if scrollable {
		hints = append([]string{"↑/↓ scroll"}, hints...)
	}
	header := title.Render("MANAGED BY CONFIG") + "  " + muted.Render(config.FormatCount(len(m.report.Resources), "resource", "resources"))
	return frame(m.width, header, strings.Join(lines, "\n"), keyHints(hints...))
}

// checkSymbol is one check's glyph, colored: passing or failed.
func checkSymbol(check config.Check) string {
	if check.OK {
		return styledSymbol(config.GlyphOK)
	}
	return styledSymbol(config.GlyphError)
}

func resourceRow(resource config.Resource) string {
	return fmt.Sprintf("%s %-20s %s", styledSymbol(resource.Symbol()), resource.Name, muted.Render(resource.Summary))
}

func (m Model) renderPlan() string {
	var lines []string
	for index, choice := range m.choices {
		line := fmt.Sprintf("%-20s %s", choice.resource.Name, actionLabel(choice.resource, choice.action()))
		lines = append(lines, focusRow(index == m.planCursor, line))
	}
	selections := planSelections(m.choices)
	if len(selections) > 0 {
		lines = append(lines, "", focusRow(m.planCursor == len(m.choices), accent.Render("Run "+config.FormatCount(len(selections), "change", "changes"))))
	}
	blocks := []string{
		title.Render("REVIEW CHANGES"),
		muted.Render("Choose inline. Nothing changes until you select Run changes."),
		strings.Join(lines, "\n"),
	}
	if m.planCursor >= 0 && m.planCursor < len(m.choices) {
		evidence := evidenceLines(m.choices[m.planCursor].resource)
		available := max(1, m.height-planChrome-len(lines))
		if len(evidence) > available {
			evidence = append(evidence[:available-1], muted.Render("… "+config.FormatCount(len(evidence)-available+1, "more detail", "more details")))
		}
		blocks = append(blocks, strings.Join(evidence, "\n"))
	}
	blocks = append(blocks, keyHints("↑/↓ move", "←/→ choose", "enter next", "esc back"))
	return frame(m.width, blocks...)
}

func actionLabel(resource config.Resource, action config.Action) string {
	if label := resource.ActionLabels[action]; label != "" {
		return accent.Render(label)
	}
	switch action {
	case config.Apply:
		if resource.Bidirectional {
			return accent.Render("Restore the saved settings")
		}
		return accent.Render("Apply the saved configuration")
	case config.Capture:
		return accent.Render("Save this Mac's settings")
	default:
		return muted.Render("Decide later")
	}
}

func evidenceLines(resource config.Resource) []string {
	var evidence []string
	for _, check := range resource.Checks {
		if check.OK {
			continue
		}
		line := checkSymbol(check) + " " + check.Label
		if check.Detail != "" {
			line += muted.Render(" — " + check.Detail)
		}
		evidence = append(evidence, line)
	}
	for _, detail := range resource.Details {
		evidence = append(evidence, accent.Render("•")+" "+detail)
	}
	if len(evidence) == 0 {
		evidence = append(evidence, muted.Render(resource.Summary))
	}
	return evidence
}

func snapshotLines(snapshot config.SnapshotStatus) []string {
	// A policy error refuses the save, and the confirmation screen offered it
	// anyway because nothing here rendered one.
	if snapshot.PolicyError != "" {
		return []string{bad.Render("Cannot save: " + snapshot.PolicyError)}
	}
	if snapshot.Dirty > 0 {
		lines := []string{fmt.Sprintf("%s → %s", config.FormatCount(snapshot.Dirty, "changed file", "changed files"), snapshot.Destination)}
		for _, change := range snapshot.Changes {
			lines = append(lines, muted.Render(change))
		}
		return lines
	}
	return []string{fmt.Sprintf("%s → %s", config.FormatCount(snapshot.Ahead, "local commit", "local commits"), snapshot.Destination)}
}

func (m Model) renderSnapshot() string {
	lines := snapshotLines(m.report.Snapshot)
	available := max(4, m.height-snapshotChrome)
	scrollable := len(lines) > available
	lines = visibleLines(lines, m.scroll, available)
	blocks := []string{title.Render("SNAPSHOT"), strings.Join(lines, "\n")}
	hints := []string{"enter save", "esc back"}
	action := "Save snapshot"
	if m.report.Snapshot.Dirty == 0 {
		action = "Publish snapshot"
	}
	blocks = append(blocks, focusRow(true, accent.Render(action)))
	if scrollable {
		hints = append([]string{"pgup/pgdown review"}, hints...)
	}
	blocks = append(blocks, keyHints(hints...))
	return frame(m.width, blocks...)
}

func (m Model) renderPrunePlanning() string {
	return frame(
		m.width,
		title.Render("CLEAN UP"),
		m.spinner.View()+" Checking unused tools and Config-owned state…",
		keyHints("ctrl+c quit"),
	)
}

func (m Model) renderPrune() string {
	lines := strings.Split(strings.Trim(m.prunePreview, "\n"), "\n")
	available := max(5, m.height-pruneChrome)
	scrollable := len(lines) > available
	lines = visibleLines(lines, m.scroll, available)
	action := "Back to dashboard"
	hints := []string{"enter back", "esc back"}
	if m.pruneHasWork {
		action = "Prune these items"
		hints = []string{"enter prune", "esc back"}
	}
	if scrollable {
		hints = append([]string{"↑/↓ scroll"}, hints...)
	}
	return frame(
		m.width,
		title.Render("CLEAN UP"),
		strings.Join(lines, "\n"),
		focusRow(true, accent.Render(action)),
		keyHints(hints...),
	)
}

func (m Model) renderRunning() string {
	output := m.operationTail(m.operation.output, max(5, m.height-runningChrome))
	if output == "" {
		output = muted.Render("Waiting for output…")
	}
	status := m.spinner.View() + " " + m.operation.label + " in progress…"
	if m.operation.cancelled {
		status = caution.Render("Cancelling…")
	}
	return frame(m.width, title.Render(strings.ToUpper(m.operation.label)), output, status, keyHints("ctrl+c cancel"))
}

func keyHints(hints ...string) string {
	return muted.Render(strings.Join(hints, "  •  "))
}

func visibleLines(lines []string, offset, available int) []string {
	maxOffset := max(0, len(lines)-available)
	offset = min(offset, maxOffset)
	return lines[offset:min(len(lines), offset+available)]
}

func outputTail(output string, available int) string {
	// Trim the blank lines around a command's output, never its indentation:
	// the pane's own step lines are indented, and their alignment and color
	// both depend on that prefix surviving.
	output = strings.Trim(output, "\n")
	if strings.TrimSpace(output) == "" {
		return ""
	}
	lines := strings.Split(output, "\n")
	if len(lines) > available {
		lines = lines[len(lines)-available:]
	}
	return strings.Join(lines, "\n")
}

// An operation narrates itself in step lines; color those glyphs the way the
// dashboard colors the same ones, and leave the rest of the pane — a driven
// command's own output, and the operation's section headings — untouched.
//
// Shape is all this has to go on, so a command's own line in the Logger's exact
// shape gets colored too, and a colored line whose style opened above the
// retained tail loses that style after the glyph. Both are cosmetic. The way
// up is provenance the pane cannot lose: operation events carrying their own
// kind, rather than a wrapped and tailed string parsed back into steps.
func (m Model) operationTail(output string, available int) string {
	lines := strings.Split(outputTail(ansi.Hardwrap(output, panelContentWidth(m.width), true), available), "\n")
	for index, line := range lines {
		if glyph, step := config.StepGlyph(line); step {
			lines[index] = strings.Replace(line, glyph, styledSymbol(glyph), 1)
		}
	}
	return strings.Join(lines, "\n")
}

// scrollBound is the largest offset the rendered screen can actually use.
// Clamping only at render time left the offset itself running past the end,
// so every keypress past it cost one dead keypress coming back.
func (m Model) scrollBound() int {
	switch m.screen {
	case screenInventory:
		return max(0, len(inventoryLines(m.report.Resources))-max(5, m.height-inventoryChrome))
	case screenSnapshot:
		return max(0, len(snapshotLines(m.report.Snapshot))-max(4, m.height-snapshotChrome))
	case screenPrune:
		lines := strings.Split(strings.Trim(m.prunePreview, "\n"), "\n")
		return max(0, len(lines)-max(5, m.height-pruneChrome))
	}
	return 0
}
