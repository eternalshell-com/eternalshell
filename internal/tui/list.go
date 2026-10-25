package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// The containers view needs things the bubbles table cannot do: a style per row
// (dimmed, bold group headers) and a cursor that skips group headers. So it
// uses this small list instead. Rows are drawn as plain text per cell, and the
// style is applied to the whole finished line, which is safe with ANSI codes.

type listColumn struct {
	title string
	width int
}

type listRow struct {
	cells  []string
	header bool   // group header: not selectable
	dim    bool   // stopped container
	warn   bool   // group header with a problem inside
	id     string // container ID, empty for headers

	// styles colors single cells (nil entries stay plain). They are applied to
	// the already padded cell text, so the column widths stay right.
	styles []*lipgloss.Style
}

type list struct {
	cols   []listColumn
	rows   []listRow
	cursor int // index into rows, always on a non-header row if there is one
	offset int // first visible row
	width  int
	height int // total height including the 2 header lines

	plain bool // no highlight for the cursor row (scrolling still follows it)
}

const listHeaderLines = 2

func (l *list) bodyHeight() int { return max(l.height-listHeaderLines, 1) }

func (l *list) setSize(width, height int) {
	l.width, l.height = width, height
	l.fixCursor(1)
}

// selectedID returns the container ID under the cursor ("" if none).
func (l *list) selectedID() string {
	if l.cursor < 0 || l.cursor >= len(l.rows) {
		return ""
	}
	return l.rows[l.cursor].id
}

// setRows replaces the rows. The cursor stays on the same container if it is
// still there, otherwise it stays at the same position (moved to a real row).
func (l *list) setRows(rows []listRow) {
	prev := l.selectedID()
	l.rows = rows
	if prev != "" {
		for i, r := range rows {
			if r.id == prev {
				l.cursor = i
				l.fixCursor(1)
				return
			}
		}
	}
	l.fixCursor(1)
}

// fixCursor puts the cursor on a selectable row: clamped into range, and moved
// in direction dir (+1 down, -1 up) if it sits on a header, then keeps it visible.
func (l *list) fixCursor(dir int) {
	if len(l.rows) == 0 {
		l.cursor, l.offset = 0, 0
		return
	}
	l.cursor = min(max(l.cursor, 0), len(l.rows)-1)
	for i := l.cursor; i >= 0 && i < len(l.rows); i += dir {
		if !l.rows[i].header {
			l.cursor = i
			l.scroll()
			return
		}
	}
	// Nothing selectable in that direction: try the other way.
	for i := l.cursor; i >= 0 && i < len(l.rows); i -= dir {
		if !l.rows[i].header {
			l.cursor = i
			l.scroll()
			return
		}
	}
	l.scroll()
}

// scroll adjusts offset so the cursor row is visible. If the row above the
// cursor is its group header, that header is kept in view when possible.
func (l *list) scroll() {
	h := l.bodyHeight()
	top := l.cursor
	if top > 0 && l.rows[top-1].header {
		top--
	}
	if l.offset > top {
		l.offset = top
	}
	if l.cursor >= l.offset+h {
		l.offset = l.cursor - h + 1
	}
	l.offset = min(max(l.offset, 0), max(len(l.rows)-h, 0))
}

func (l *list) move(dir int) {
	if len(l.rows) == 0 {
		return
	}
	next := l.cursor + dir
	for next >= 0 && next < len(l.rows) && l.rows[next].header {
		next += dir
	}
	if next < 0 || next >= len(l.rows) {
		return // already at the first/last container
	}
	l.cursor = next
	l.scroll()
}

func (l *list) page(dir int) {
	l.cursor = min(max(l.cursor+dir*l.bodyHeight(), 0), max(len(l.rows)-1, 0))
	l.fixCursor(dir)
}

func (l *list) top() { l.cursor = 0; l.fixCursor(1) }

func (l *list) bottom() { l.cursor = len(l.rows) - 1; l.fixCursor(-1) }

// line joins cells into one text line with fixed column widths.
func (l *list) line(cells []string) string {
	return " " + strings.Join(l.pad(cells), "  ")
}

// pad cuts every cell to its column width and fills it up with spaces.
func (l *list) pad(cells []string) []string {
	parts := make([]string, len(l.cols))
	for i, c := range l.cols {
		text := ""
		if i < len(cells) {
			text = cells[i]
		}
		parts[i] = runewidth.FillRight(runewidth.Truncate(text, c.width, "…"), c.width)
	}
	return parts
}

// styledLine is line with the cell colors of a row.
func (l *list) styledLine(r listRow) string {
	parts := l.pad(r.cells)
	for i := range parts {
		if i < len(r.styles) && r.styles[i] != nil {
			parts[i] = r.styles[i].Render(parts[i])
		}
	}
	return " " + strings.Join(parts, "  ")
}

func (l *list) view() string {
	titles := make([]string, len(l.cols))
	for i, c := range l.cols {
		titles[i] = c.title
	}
	var b strings.Builder
	b.WriteString(dimStyle.Render(l.line(titles)))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(strings.Repeat("─", max(l.width, 1))))

	h := l.bodyHeight()
	for i := 0; i < h; i++ {
		b.WriteString("\n")
		idx := l.offset + i
		if idx >= len(l.rows) {
			continue
		}
		r := l.rows[idx]
		text := l.line(r.cells)
		if len(r.styles) > 0 {
			text = l.styledLine(r)
		}
		switch {
		case len(r.styles) > 0:
			// colored cells: no whole-line style, it would reset the cell colors
		case idx == l.cursor && !r.header && !l.plain:
			text = selectedStyle.Render(text)
		case r.header && r.warn:
			text = headerWarnStyle.Render(text)
		case r.header:
			text = headerStyle.Render(text)
		case r.dim:
			text = dimStyle.Render(text)
		}
		b.WriteString(text)
	}
	return lipgloss.NewStyle().MaxWidth(max(l.width, 1)).Render(b.String())
}
