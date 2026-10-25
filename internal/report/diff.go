package report

import "sort"

// Changes is what differs between the previous report and now.
type Changes struct {
	HasPrev bool // false: there is no previous report to compare

	MountUsed  map[string]int64 // change of used bytes, for mounts in both
	NewMounts  []string
	GoneMounts []string

	DiskCompared bool             // both reports have Docker disk figures
	ProjectDelta map[string]int64 // change per project key, for keys in both
	NewProjects  []string         // project keys with disk use that were not there
	ReclaimDelta int64

	AddedGroups       []string // compose projects (or the standalone group) that are new
	RemovedGroups     []string
	AddedContainers   []string // "group/name", inside groups that exist in both
	RemovedContainers []string

	Rebooted bool // the machine booted after the previous report
}

// Diff compares the previous state with the current one. With no previous state
// the result only says so.
func Diff(prev, cur *State) Changes {
	var c Changes
	if prev == nil || cur == nil {
		return c
	}
	c.HasPrev = true

	// Mounts
	c.MountUsed = map[string]int64{}
	for path, now := range cur.Mounts {
		if before, ok := prev.Mounts[path]; ok {
			c.MountUsed[path] = int64(now.Used) - int64(before.Used)
		} else {
			c.NewMounts = append(c.NewMounts, path)
		}
	}
	for path := range prev.Mounts {
		if _, ok := cur.Mounts[path]; !ok {
			c.GoneMounts = append(c.GoneMounts, path)
		}
	}

	// Docker disk use per project
	if prev.DiskKnown && cur.DiskKnown {
		c.DiskCompared = true
		c.ProjectDelta = map[string]int64{}
		for key, now := range cur.Projects {
			if before, ok := prev.Projects[key]; ok {
				c.ProjectDelta[key] = now - before
			} else {
				c.NewProjects = append(c.NewProjects, key)
			}
		}
		c.ReclaimDelta = cur.Reclaimable - prev.Reclaimable
	}

	// Containers
	for group, names := range cur.Containers {
		before, ok := prev.Containers[group]
		if !ok {
			c.AddedGroups = append(c.AddedGroups, group)
			continue
		}
		c.AddedContainers = append(c.AddedContainers, missing(names, before, group)...)
		c.RemovedContainers = append(c.RemovedContainers, missing(before, names, group)...)
	}
	for group := range prev.Containers {
		if _, ok := cur.Containers[group]; !ok {
			c.RemovedGroups = append(c.RemovedGroups, group)
		}
	}

	c.Rebooted = !prev.Time.IsZero() && cur.BootTime.After(prev.Time)

	for _, s := range [][]string{c.NewMounts, c.GoneMounts, c.NewProjects, c.AddedGroups, c.RemovedGroups, c.AddedContainers, c.RemovedContainers} {
		sort.Strings(s)
	}
	return c
}

// missing returns the names in a that are not in b, as "group/name".
func missing(a, b []string, group string) []string {
	have := make(map[string]bool, len(b))
	for _, n := range b {
		have[n] = true
	}
	var out []string
	for _, n := range a {
		if !have[n] {
			out = append(out, group+"/"+n)
		}
	}
	return out
}
