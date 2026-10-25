package tui

import (
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// All colors of the UI are defined here, and every style is built from them.
// One accent color (purple); green, yellow and red only carry a status.
//
// With NO_COLOR set, or when the output is not a terminal, lipgloss prints no
// colors at all. Meaning therefore never lives in color alone: see plainMode.
var (
	accent   = lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#A78BFA"}
	onAccent = lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#1E1B4B"} // text on an accent background
	selBg    = lipgloss.AdaptiveColor{Light: "#EDE9FE", Dark: "#3B2F63"} // selected row
	dim      = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"}
	green    = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#4ADE80"}
	yellow   = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FACC15"}
	red      = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}
)

var (
	dimStyle        = lipgloss.NewStyle().Foreground(dim)
	accentStyle     = lipgloss.NewStyle().Foreground(accent)
	titleStyle      = lipgloss.NewStyle().Bold(true).Foreground(accent)
	headerStyle     = lipgloss.NewStyle().Bold(true).Foreground(accent) // project rows
	headerWarnStyle = lipgloss.NewStyle().Bold(true).Foreground(red)    // project rows with a problem
	infoStyle       = lipgloss.NewStyle().Foreground(dim)
	footerStyle     = lipgloss.NewStyle().Foreground(dim)
	keyStyle        = lipgloss.NewStyle().Bold(true).Foreground(accent) // keys in the footer
	tabActive       = lipgloss.NewStyle().Bold(true).Foreground(onAccent).Background(accent)
	tabInactive     = lipgloss.NewStyle().Foreground(dim)
	borderStyle     = lipgloss.NewStyle().Foreground(dim)
	greenCell       = lipgloss.NewStyle().Foreground(green)
	yellowCell      = lipgloss.NewStyle().Foreground(yellow)
	redCell         = lipgloss.NewStyle().Foreground(red)
	errorStyle      = lipgloss.NewStyle().Foreground(red)
	emptyViewStyle  = lipgloss.NewStyle().Foreground(dim).PaddingLeft(1)
)

// levelColor returns the style for a usage level (bars).
func levelColor(l usageLevel) lipgloss.Style {
	switch l {
	case levelHigh:
		return redCell
	case levelMid:
		return yellowCell
	default:
		return greenCell
	}
}

// plainMode reports whether there are no colors. Then shapes have to say what
// colors normally say: the selected row gets a marker, the active tab brackets,
// and the status dots differ in shape. All of them take the same width as the
// colored version, so the layout does not move.
func plainMode() bool { return lipgloss.ColorProfile() == termenv.Ascii }

// tableStyles is a quiet table: a dim header with a line below, and the
// selected row on a tinted background so scrolling is visible.
func tableStyles() table.Styles {
	s := table.DefaultStyles()
	s.Header = s.Header.
		Foreground(dim).
		BorderStyle(lipgloss.NormalBorder()).
		BorderBottom(true).
		BorderForeground(dim)
	s.Selected = lipgloss.NewStyle().Bold(true).Background(selBg)
	return s
}
