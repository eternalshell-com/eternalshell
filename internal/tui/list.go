package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// The containers and domains views need things the bubbles table cannot do: a
// style per row (dimmed, group headers), colored single cells and a cursor that
// skips group headers. So they use this small list instead.
//
// Each cell is cut and padded to its column width as plain text first, and only
// then styled, so ANSI codes never disturb the widths.

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

	// styles colors single cells (nil entries use the style of the row).
	styles []*lipgloss.Style
}

type list struct {
	cols   []listColumn
	rows   []listRow
	cursor int // index into rows, always on a non-header row if there is one
	offset int // first visible row
	width  int
	height int // total height including the 2 header lines
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

// visible returns the indexes of the columns that are shown. A column with a
// width of 0 is hidden, and takes no gap either.
func (l *list) visible() []int {
	var out []int
	for i, c := range l.cols {
		if c.width > 0 {
			out = append(out, i)
		}
	}
	return out
}

// rowBase is the style of a whole row before its cells are colored.
func rowBase(r listRow) lipgloss.Style {
	switch {
	case r.header && r.warn:
		return headerWarnStyle
	case r.header:
		return headerStyle
	case r.dim:
		return dimStyle
	}
	return lipgloss.NewStyle()
}

// renderLine draws one row: a margin, the cells with their gaps, and spaces up
// to the full width. The selected row gets a background over all of it, so it
// reads as one band; without colors it starts with a marker instead.
func (l *list) renderLine(cells []string, styles []*lipgloss.Style, base lipgloss.Style, selected bool) string {
	if selected {
		base = base.Background(selBg).Bold(true)
	}
	lead := " "
	if selected && plainMode() {
		lead = "▸"
	}

	var b strings.Builder
	b.WriteString(base.Render(lead))
	used := 1
	for n, i := range l.visible() {
		if n > 0 {
			b.WriteString(base.Render(strings.Repeat(" ", colGap)))
			used += colGap
		}
		c := l.cols[i]
		text := ""
		if i < len(cells) {
			text = cells[i]
		}
		text = runewidth.FillRight(runewidth.Truncate(text, c.width, "…"), c.width)

		st := base
		if i < len(styles) && styles[i] != nil {
			st = *styles[i]
			if selected {
				st = st.Background(selBg).Bold(true)
			}
		}
		b.WriteString(st.Render(text))
		used += c.width
	}
	if rest := l.width - used; rest > 0 {
		b.WriteString(base.Render(strings.Repeat(" ", rest)))
	}
	return b.String()
}

func (l *list) view() string {
	titles := make([]string, len(l.cols))
	for i, c := range l.cols {
		titles[i] = c.title
	}
	var b strings.Builder
	b.WriteString(l.renderLine(titles, nil, dimStyle, false))
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
		b.WriteString(l.renderLine(r.cells, r.styles, rowBase(r), idx == l.cursor && !r.header))
	}
	return lipgloss.NewStyle().MaxWidth(max(l.width, 1)).Render(b.String())
}
