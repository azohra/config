package ui

import (
	"testing"

	"charm.land/lipgloss/v2"
)

func TestFrameFitsTerminal(t *testing.T) {
	for _, width := range []int{20, 40, 80, 120} {
		got := frame(width, "A deliberately long line that should wrap inside the panel without overflowing the terminal width.")
		if rendered := lipgloss.Width(got); rendered > width {
			t.Fatalf("frame width = %d for %d-column terminal", rendered, width)
		}
	}
}
