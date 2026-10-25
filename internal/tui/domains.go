package tui

import (
	"context"
	"fmt"
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"eternalshell/internal/collector"
	"eternalshell/internal/domains"
)

const (
	// recheckEvery is how often the domains are checked again while the view is shown.
	recheckEvery = 5 * time.Minute

	// minRecheck is the shortest time between two checks, so opening the view
	// again and again does not send a request each time.
	minRecheck = 60 * time.Second

	// checkCap bounds one whole check run, however many hosts there are.
	checkCap = 60 * time.Second
)

// domainsState is everything the Domains view remembers.
type domainsState struct {
	opened    bool // the view has been opened once; nothing is checked before that
	running   bool
	lastStart time.Time
	checkedAt time.Time // when the latest results arrived
	results   map[string]domains.Result
}

// Messages of the Domains view.
type (
	domainsMsg struct {
		results map[string]domains.Result
		at      time.Time
	}
	recheckMsg struct{}
)

// domainHost is a domain and where it comes from.
type domainHost struct {
	Host    string
	Project string // compose project, or the container name if standalone
}

// domainHosts lists the hosts of all containers (running or not), each host
// once. The first container in the list wins, which is the order of the
// containers view.
func domainHosts(cs []collector.Container) []domainHost {
	seen := map[string]bool{}
	var out []domainHost
	for _, c := range cs {
		for _, h := range c.Hosts {
			if seen[h] {
				continue
			}
			seen[h] = true
			project := c.Project
			if project == "" {
				project = c.Name
			}
			out = append(out, domainHost{Host: h, Project: project})
		}
	}
	return out
}

// Domain column widths. PROJECT takes the room its longest name needs, TARGET
// gets what is left and disappears when there is not enough.
const (
	domStatusW   = 9
	domCertW     = 23
	domProjectMn = 7
	domProjectMx = 24
	domTargetMn  = 8
	domTargetMx  = 28
	domDomainMn  = 24
)

// domainColumns sizes the columns for a list of the given width. projectW is
// the width of the longest project name.
func domainColumns(width, projectW int) []listColumn {
	width -= rightMargin
	projectW = min(max(projectW, domProjectMn), domProjectMx)
	fixed := domStatusW + domCertW + projectW

	// With TARGET there are four gaps, without it three, plus the margin.
	avail := width - 1 - 4*colGap - fixed
	target := 0
	if avail-domDomainMn >= domTargetMn {
		target = min(avail-domDomainMn, domTargetMx)
	} else {
		avail = width - 1 - 3*colGap - fixed
	}
	domain := avail
	if target > 0 {
		domain = avail - target
	}
	return []listColumn{
		{"DOMAIN", max(domain, 12)},
		{"PROJECT", projectW},
		{"STATUS", domStatusW},
		{"TARGET", target},
		{"CERT", domCertW},
	}
}

// domainRows builds the rows: problems first (red), then warnings (yellow),
// then the rest, each group alphabetical. A host without a result yet shows "…".
func domainRows(hosts []domainHost, results map[string]domains.Result, now time.Time) []listRow {
	type entry struct {
		host domainHost
		res  domains.Result
		have bool
		rank int
	}
	entries := make([]entry, 0, len(hosts))
	for _, h := range hosts {
		e := entry{host: h, rank: 2}
		if r, ok := results[h.Host]; ok {
			e.res, e.have, e.rank = r, true, r.Rank(now)
		}
		entries = append(entries, e)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].rank != entries[j].rank {
			return entries[i].rank < entries[j].rank
		}
		return entries[i].host.Host < entries[j].host.Host
	})

	rows := make([]listRow, 0, len(entries))
	for _, e := range entries {
		row := listRow{id: e.host.Host, cells: []string{e.host.Host, e.host.Project, "…", "", "…"}}
		if e.have {
			row.cells[2] = e.res.StatusText()
			row.cells[3] = e.res.Location // only a 3xx response has one
			row.cells[4] = e.res.CertText(now)
			row.styles = []*lipgloss.Style{nil, nil, levelStyle(e.res.StatusLevel()), nil, levelStyle(e.res.CertLevel(now))}
		}
		rows = append(rows, row)
	}
	return rows
}

// levelStyle returns the cell color for a level: red for bad, yellow for a
// warning, nothing otherwise.
func levelStyle(l domains.Level) *lipgloss.Style {
	switch l {
	case domains.LevelBad:
		return &redCell
	case domains.LevelWarn:
		return &yellowCell
	default:
		return nil
	}
}

// startDomainCheck returns the command that checks the given hosts in the
// background, or nil if a check is not due. It never blocks the UI.
func (m *Model) startDomainCheck(now time.Time) tea.Cmd {
	if m.domains.running || m.snap == nil {
		return nil
	}
	if !m.domains.lastStart.IsZero() && now.Sub(m.domains.lastStart) < minRecheck {
		return nil
	}
	hosts := domainHosts(m.snap.Containers)
	if len(hosts) == 0 {
		return nil
	}
	m.domains.running = true
	m.domains.lastStart = now

	names := make([]string, len(hosts))
	for i, h := range hosts {
		names[i] = h.Host
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), checkCap)
		defer cancel()
		return domainsMsg{results: domains.CheckAll(ctx, names), at: time.Now()}
	}
}

func scheduleRecheck() tea.Cmd {
	return tea.Tick(recheckEvery, func(time.Time) tea.Msg { return recheckMsg{} })
}

// openDomains is what pressing 3 does: show the view, and start the first check
// (and the 5-minute timer) the first time.
func (m Model) openDomains() (tea.Model, tea.Cmd) {
	m.view = viewDomains
	first := !m.domains.opened
	m.domains.opened = true
	m.refreshDomainRows()

	cmds := []tea.Cmd{m.startDomainCheck(time.Now())}
	if first {
		cmds = append(cmds, scheduleRecheck())
	}
	return m, tea.Batch(cmds...)
}

func (m *Model) refreshDomainRows() {
	if m.snap == nil {
		return
	}
	hosts := domainHosts(m.snap.Containers)
	longest := 0
	for _, h := range hosts {
		longest = max(longest, runewidth.StringWidth(h.Project))
	}
	m.domainList.cols = domainColumns(m.innerWidth(), longest)
	m.domainList.setRows(domainRows(hosts, m.domains.results, time.Now()))
}

// domainSummaryView is the line under the table in the Domains view.
func (m Model) domainSummaryView() string {
	var text string
	switch {
	case m.snap == nil:
		text = " domains: collecting…"
	default:
		hosts := domainHosts(m.snap.Containers)
		problems := 0
		now := time.Now()
		for _, h := range hosts {
			if r, ok := m.domains.results[h.Host]; ok && r.Rank(now) == 0 {
				problems++
			}
		}
		switch {
		case len(hosts) == 0:
			text = " domains: no Traefik host rules found in the container labels"
		case m.domains.checkedAt.IsZero():
			text = fmt.Sprintf(" domains: %d hosts · checking…", len(hosts))
		default:
			text = fmt.Sprintf(" domains: %d hosts · %d problems · checked %s ago",
				len(hosts), problems, ago(now.Sub(m.domains.checkedAt)))
			if m.domains.running {
				text += " · checking again…"
			}
		}
	}
	return infoStyle.MaxWidth(max(m.width, 1)).Render(text)
}

// ago formats a duration as "30 s", "4 min" or "2 h".
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	default:
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
}
