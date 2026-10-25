// Package mailhtml turns a small document model into the HTML part of a mail.
//
// The builders (report, alerts, test mail) describe what to show with Doc,
// Section and Block, and one template draws it. Everything dynamic goes through
// html/template, which escapes it. Colors and widths never come from data: each
// level, tone and fill maps to a fixed constant below.
//
// The HTML is made to survive mail clients: layout tables, inline CSS only, no
// images, fonts, scripts or links, and an explicit background color wherever
// there is a text color, so a client that darkens mails still gets readable
// pairs.
package mailhtml

import (
	"fmt"
	"html/template"
	"os"
	"time"
)

// Level is how serious something is; it decides the color of a badge or bullet.
type Level int

const (
	LevelNone Level = iota // neutral
	LevelOK                // green
	LevelWarn              // orange
	LevelBad               // red
)

// Color is the fixed color of a level. Badges use it as background with white
// text, bullets as text color on white; all of them pass 4.5:1 contrast.
func (l Level) Color() template.CSS {
	switch l {
	case LevelOK:
		return "#15803D"
	case LevelWarn:
		return "#C2410C"
	case LevelBad:
		return "#B91C1C"
	}
	return "#6B7280"
}

// Tone colors a value. Changes are colored by whether use grew: growth is red,
// shrinking green.
type Tone int

const (
	ToneNormal Tone = iota
	ToneMuted       // grey: "no change", side notes
	ToneUp          // red: grew
	ToneDown        // green: shrank
)

// Color is the fixed text color of a tone.
func (t Tone) Color() template.CSS {
	switch t {
	case ToneMuted:
		return "#6B7280"
	case ToneUp:
		return "#B91C1C"
	case ToneDown:
		return "#15803D"
	}
	return "#1F2937"
}

// Fill is the color of the filled part of a usage bar.
type Fill int

const (
	FillAccent Fill = iota // purple
	FillWarn               // orange
	FillBad                // red
)

// Color is the fixed color of a fill.
func (f Fill) Color() template.CSS {
	switch f {
	case FillWarn:
		return "#F59E0B"
	case FillBad:
		return "#DC2626"
	}
	return "#7C3AED"
}

// MeterFill picks the color of a usage bar: red from the alert threshold, orange
// from 80 %, purple below. Red wins when the threshold is below 80.
func MeterFill(percent, alertPercent float64) Fill {
	switch {
	case alertPercent > 0 && percent >= alertPercent:
		return FillBad
	case percent >= 80:
		return FillWarn
	}
	return FillAccent
}

// Doc is one mail.
type Doc struct {
	Server   string    // shown in the header bar
	Host     string    // the machine that sent it, for the footer
	SentAt   time.Time // for the footer
	Badge    Badge
	Title    string // the headline under the badge
	Lead     string // a line of smaller text under the headline
	Sections []Section
}

// New starts a document for a server; the host name comes from the machine.
func New(server string, sentAt time.Time) Doc {
	host, _ := os.Hostname()
	return Doc{Server: server, Host: host, SentAt: sentAt}
}

// Footer is the small grey line at the bottom.
func (d Doc) Footer() string {
	return fmt.Sprintf("Sent by eternalshell on %s at %s", d.Host, d.SentAt.Format("2006-01-02 15:04 MST"))
}

// Badge is the status pill at the top: "All OK", "2 things need attention", ...
type Badge struct {
	Level Level
	Text  string
}

// Section is a card with a heading.
type Section struct {
	Heading string
	Blocks  []Block
}

// Block is one piece of a card. Use the constructors below.
type Block struct {
	Kind   string // "text", "note", "list", "facts", "table" or "meters"
	Text   string
	Items  []Item
	Facts  []Fact
	Table  *Table
	Meters []Meter
}

// Text is a paragraph.
func Text(s string) Block { return Block{Kind: "text", Text: s} }

// Note is small grey text.
func Note(s string) Block { return Block{Kind: "note", Text: s} }

// List is a bulleted list.
func List(items ...Item) Block { return Block{Kind: "list", Items: items} }

// Facts is a list of label and value rows.
func Facts(facts ...Fact) Block { return Block{Kind: "facts", Facts: facts} }

// TableOf is a table.
func TableOf(cols []Col, rows [][]Cell) Block {
	return Block{Kind: "table", Table: &Table{Cols: cols, Rows: rows}}
}

// Meters is a list of usage bars.
func Meters(m ...Meter) Block { return Block{Kind: "meters", Meters: m} }

// Item is one bullet; its level colors the dot.
type Item struct {
	Text  string
	Level Level
}

// Fact is one label and value row. Mono sets the value in a monospace font
// (numbers, sizes, commands).
type Fact struct {
	Label, Value string
	Mono         bool
	Tone         Tone
}

// Col is a table column.
type Col struct {
	Title string
	Right bool // right-aligned, for numbers
}

// Cell is one table cell. Sub is a smaller grey second line.
type Cell struct {
	Text, Sub string
	Mono      bool
	Tone      Tone
	Right     bool // filled in from the column when the document is rendered
}

// Table is a table with a header row.
type Table struct {
	Cols []Col
	Rows [][]Cell
}

// Meter is a usage bar with a label, a detail line and an optional change.
type Meter struct {
	Label      string  // e.g. the mount point
	Detail     string  // e.g. "41.2G of 100.0G"
	Percent    float64 // 0..100
	Fill       Fill
	Change     string // e.g. "+1.2G", empty if none
	ChangeTone Tone
}

// Pct is the percentage as a whole number between 0 and 100 for the bar, at
// least 1 when there is any use so the bar stays visible.
func (m Meter) Pct() int {
	switch {
	case m.Percent <= 0:
		return 0
	case m.Percent >= 100:
		return 100
	case m.Percent < 1:
		return 1
	}
	return int(m.Percent + 0.5)
}

// PctText is the percentage as text for the right end of the label row.
func (m Meter) PctText() string { return fmt.Sprintf("%.0f%%", m.Percent) }
