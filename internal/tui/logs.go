package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// logsState is the log screen that opens when Enter is pressed on a container.
// The log text lives only in memory (in this struct and the viewport); it is
// never written to disk.
type logsState struct {
	active  bool
	id      string
	name    string
	loading bool
	err     error
	text    string // sanitized by the collector; wrapped to the width on display
	vp      viewport.Model
}

// logsMsg carries the result of a log request back to Update.
type logsMsg struct {
	id   string
	text string
	err  error
}

// fetchLogs returns a command that loads the logs in the background.
func (m Model) fetchLogs(id string) tea.Cmd {
	col := m.col
	return func() tea.Msg {
		text, err := col.Logs(id)
		return logsMsg{id: id, text: text, err: err}
	}
}

// openLogs switches to the log screen for the container under the cursor.
// On a group header (or with no containers) nothing happens.
func (m Model) openLogs() (tea.Model, tea.Cmd) {
	id := m.containers.selectedID()
	if id == "" || m.snap == nil {
		return m, nil
	}
	name := id[:min(len(id), 12)]
	for _, c := range m.snap.Containers {
		if c.ID == id {
			name = c.Name
		}
	}
	m.logs = logsState{active: true, id: id, name: name, loading: true}
	m.layoutLogs()
	return m, m.fetchLogs(id)
}

// updateLogsKey handles a key press while the log screen is open.
func (m Model) updateLogsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.logs.active = false
	case "1":
		m.logs.active, m.view = false, viewContainers
	case "2":
		m.logs.active, m.view = false, viewProcesses
	case "r":
		if m.logs.loading {
			return m, nil
		}
		m.logs.loading = true
		return m, m.fetchLogs(m.logs.id)
	case "home":
		m.logs.vp.GotoTop()
	case "end":
		m.logs.vp.GotoBottom()
	default: // arrows, page up/down: the viewport scrolls itself
		var cmd tea.Cmd
		m.logs.vp, cmd = m.logs.vp.Update(msg)
		return m, cmd
	}
	return m, nil
}

// handleLogsMsg stores a finished log request. A result for a container that
// is no longer shown (the user went back already) is ignored.
func (m Model) handleLogsMsg(msg logsMsg) (tea.Model, tea.Cmd) {
	if !m.logs.active || msg.id != m.logs.id {
		return m, nil
	}
	m.logs.loading = false
	m.logs.err = msg.err
	if msg.err == nil {
		m.logs.text = msg.text
		m.layoutLogs()
		m.logs.vp.GotoBottom() // newest lines are at the bottom
	}
	return m, nil
}

// layoutLogs sizes the viewport and (re)wraps the text to the screen width.
func (m *Model) layoutLogs() {
	h := max(m.height-chromeLines, 4)
	if m.logs.vp.Width != m.width || m.logs.vp.Height != h {
		m.logs.vp = viewport.New(m.width, h)
	}
	text := strings.TrimRight(m.logs.text, "\n")
	if text == "" && !m.logs.loading && m.logs.err == nil {
		text = "(no log output)"
	}
	m.logs.vp.SetContent(lipgloss.NewStyle().Width(max(m.width, 1)).Render(text))
}

func (m Model) logsTitleView() string {
	title := fmt.Sprintf(" Logs · %s", m.logs.name)
	hint := fmt.Sprintf(" · last %d lines, newest at the bottom", logLinesShown)
	if m.logs.loading {
		hint = " · loading…"
	}
	return tabActive.Render(title) + tabInactive.Render(hint)
}

func (m Model) logsBodyView() string {
	if m.logs.err != nil {
		return m.message(errorStyle.Render(fmt.Sprintf("Could not read the logs: %v", m.logs.err)))
	}
	if m.logs.loading && m.logs.text == "" {
		return m.message("Loading logs…")
	}
	return m.logs.vp.View()
}

func (m Model) logsFooterView() string {
	return footerStyle.Render(" ↑/↓ scroll · r reload · esc back · q quit")
}
