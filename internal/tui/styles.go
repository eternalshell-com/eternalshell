package tui

import (
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"
)

var (
	dim    = lipgloss.AdaptiveColor{Light: "243", Dark: "245"}
	accent = lipgloss.AdaptiveColor{Light: "25", Dark: "81"}
	warn   = lipgloss.AdaptiveColor{Light: "160", Dark: "203"}

	dimStyle        = lipgloss.NewStyle().Foreground(dim)
	selectedStyle   = lipgloss.NewStyle().Bold(true).Foreground(accent)
	headerStyle     = lipgloss.NewStyle().Bold(true)
	headerWarnStyle = lipgloss.NewStyle().Bold(true).Foreground(warn)
	titleStyle      = lipgloss.NewStyle().Bold(true).Foreground(accent)
	infoStyle       = lipgloss.NewStyle().Foreground(dim)
	tabActive       = lipgloss.NewStyle().Bold(true).Foreground(accent)
	tabInactive     = lipgloss.NewStyle().Foreground(dim)
	footerStyle     = lipgloss.NewStyle().Foreground(dim)
	errorStyle      = lipgloss.NewStyle().Foreground(warn)
	emptyViewStyle  = lipgloss.NewStyle().Foreground(dim).PaddingLeft(1)
)

// tableStyles is a quiet table: bold header with a line below, and a subtle
// cursor row so scrolling is visible.
func tableStyles() table.Styles {
	s := table.DefaultStyles()
	s.Header = s.Header.
		Foreground(dim).
		BorderStyle(lipgloss.NormalBorder()).
		BorderBottom(true).
		BorderForeground(dim)
	s.Selected = lipgloss.NewStyle().Bold(true).Foreground(accent)
	return s
}
