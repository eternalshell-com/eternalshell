package collector

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// This file holds the rules that say which containers need attention. The TUI
// and the status report both use them, so they agree.

var exitedRe = regexp.MustCompile(`^Exited \((\d+)\)`)

// ExitCode extracts the exit code from a Docker status like "Exited (143) 6 months ago".
// ok is false if the status is not an "Exited" status.
func ExitCode(status string) (code int, ok bool) {
	m := exitedRe.FindStringSubmatch(status)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// Health reads the health state out of a Docker status like "Up 2 weeks (healthy)".
func Health(status string) (healthy, unhealthy bool) {
	return strings.Contains(status, "(healthy)"), strings.Contains(status, "(unhealthy)")
}

// ExitedBadly reports whether a stopped container ended in a way that needs a
// look: an exit code other than 0, or 143 (SIGTERM, which is what a normal
// `docker stop` produces).
func (c Container) ExitedBadly() bool {
	if c.State != "exited" {
		return false
	}
	code, ok := ExitCode(c.Status)
	return !ok || (code != 0 && code != 143)
}

// Attention says why a container needs attention, or returns "" if it is fine:
// dead, restarting, exited badly or unhealthy. A container that was stopped
// cleanly is fine.
func (c Container) Attention() string {
	switch {
	case c.State == "dead":
		return "dead"
	case c.State == "restarting":
		return "restarting"
	case c.ExitedBadly():
		if code, ok := ExitCode(c.Status); ok {
			return fmt.Sprintf("exited (code %d)", code)
		}
		return "exited"
	case c.Running():
		if _, unhealthy := Health(c.Status); unhealthy {
			return "unhealthy"
		}
	}
	return ""
}

// HostOwner is a domain and the project it belongs to.
type HostOwner struct {
	Host    string
	Project string // compose project, or the container name if standalone
}

// HostOwners lists the domains of all containers (running or not), each host
// once. The first container in the list wins.
func HostOwners(cs []Container) []HostOwner {
	seen := map[string]bool{}
	var out []HostOwner
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
			out = append(out, HostOwner{Host: h, Project: project})
		}
	}
	return out
}
