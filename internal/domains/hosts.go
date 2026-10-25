// Package domains finds the domains a server hosts (from Traefik labels on the
// containers) and checks each one: HTTP status, redirect and certificate.
package domains

import (
	"net/netip"
	"regexp"
	"sort"
	"strings"
)

var (
	// hostCallRe finds one Host(...) call and captures its arguments. The word
	// has to be followed directly by "(": HostRegexp( and the TCP HostSNI( do
	// not match. Traefik v3 has one host per call, v2 may list several.
	hostCallRe = regexp.MustCompile(`\bHost\(([^)]*)\)`)

	// quotedRe finds the backtick-quoted strings inside the arguments.
	quotedRe = regexp.MustCompile("`([^`]+)`")

	// hostnameRe accepts plain hostnames only. Labels are written by whoever
	// builds a container, so anything odd (control characters, wildcards,
	// leftover {templates}) is rejected instead of being requested or printed.
	hostnameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$`)
)

// HostsFromRule returns the hosts named by Host(`...`) calls in a Traefik rule,
// for example "Host(`a.com`) || Host(`b.com`, `c.com`)". The result is
// lowercase, deduplicated and sorted; unusable names are left out.
func HostsFromRule(rule string) []string {
	seen := map[string]bool{}
	for _, call := range hostCallRe.FindAllStringSubmatch(rule, -1) {
		for _, q := range quotedRe.FindAllStringSubmatch(call[1], -1) {
			if h := cleanHost(q[1]); h != "" {
				seen[h] = true
			}
		}
	}
	return sortedHosts(seen)
}

// HostsFromLabels returns the hosts of every label whose key ends in ".rule"
// (traefik.http.routers.<name>.rule and similar).
func HostsFromLabels(labels map[string]string) []string {
	seen := map[string]bool{}
	for key, value := range labels {
		if !strings.HasSuffix(key, ".rule") {
			continue
		}
		for _, h := range HostsFromRule(value) {
			seen[h] = true
		}
	}
	return sortedHosts(seen)
}

// cleanHost normalizes a host and returns "" if it must not be checked: not a
// plain hostname, an IP address (so a label cannot point the check at an
// internal service by address), or localhost.
func cleanHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimSuffix(h, ".")
	if h == "" || len(h) > 253 || !hostnameRe.MatchString(h) {
		return ""
	}
	if _, err := netip.ParseAddr(h); err == nil {
		return ""
	}
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return ""
	}
	return h
}

func sortedHosts(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}
