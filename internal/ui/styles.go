package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	config "github.com/azohra/config/internal/config"
)

var (
	foreground  = lipgloss.Color("#fbf1c7")
	mutedColor  = lipgloss.Color("#a89984")
	borderColor = lipgloss.Color("#665c54")
	greenColor  = lipgloss.Color("#b8bb26")
	redColor    = lipgloss.Color("#fb4934")
	yellowColor = lipgloss.Color("#fabd2f")
	aquaColor   = lipgloss.Color("#8ec07c")

	title       = lipgloss.NewStyle().Bold(true).Foreground(yellowColor)
	muted       = lipgloss.NewStyle().Foreground(mutedColor)
	good        = lipgloss.NewStyle().Foreground(greenColor)
	bad         = lipgloss.NewStyle().Foreground(redColor)
	caution     = lipgloss.NewStyle().Foreground(yellowColor)
	accent      = lipgloss.NewStyle().Foreground(aquaColor)
	selected    = lipgloss.NewStyle().Bold(true).Foreground(foreground)
	cursorStyle = lipgloss.NewStyle().Foreground(yellowColor)
	panel       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(borderColor).Padding(1, 2)
)

// severityStyle is the one place a glyph's color is decided, so every surface
// that shows a severity agrees. A glyph with no color of its own renders plain.
func severityStyle(symbol string) lipgloss.Style {
	switch symbol {
	case config.GlyphOK:
		return good
	case config.GlyphInfo:
		return accent
	case config.GlyphWarn, config.GlyphChoice:
		return caution
	case config.GlyphError:
		return bad
	}
	return lipgloss.NewStyle()
}

func styledSymbol(symbol string) string {
	return severityStyle(symbol).Render(symbol)
}

// styledKind colors a typed span the way the glyph for its severity is
// colored, so config's event kinds need no second palette of their own.
func styledKind(kind config.OperationEventKind, value string) string {
	glyph, ok := kind.Glyph()
	if !ok {
		return value
	}
	return severityStyle(glyph).Render(value)
}

func frame(width int, blocks ...string) string {
	panelWidth := panelContentWidth(width)
	contentWidth := max(1, panelWidth-panel.GetHorizontalFrameSize())
	visible := nonempty(blocks)
	for index, block := range visible {
		visible[index] = ansi.Wordwrap(block, contentWidth, "")
	}
	return panel.Width(panelWidth).Render(strings.Join(visible, "\n\n"))
}

func panelContentWidth(terminalWidth int) int {
	// Two border cells, four padding cells, and a one-cell margin on each side.
	return min(max(1, terminalWidth-8), 86)
}

func nonempty(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, value)
		}
	}
	return result
}
