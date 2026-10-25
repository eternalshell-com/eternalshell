package collector

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const (
	// diskEvery is how often the disk usage is refreshed. It is much slower than
	// the sampling interval on purpose: Docker's disk usage call can take seconds.
	diskEvery = 5 * time.Minute

	// diskRetryAfter is the wait after a failed refresh (Docker not reachable).
	diskRetryAfter = time.Minute

	// diskTimeout bounds one whole refresh: the Docker calls, the log sizes and
	// walking the bind-mount directories.
	diskTimeout = 2 * time.Minute
)

// sizeInfo is a size that may be incomplete (unreadable parts, timeout).
type sizeInfo struct {
	Bytes int64
	Known bool
}

// DiskParts is the disk space of one thing, split by where it lives. A part is
// marked unknown when it could not be measured completely; the bytes then hold
// only what was measured.
type DiskParts struct {
	Layers  int64 // writable container layers
	Volumes int64 // Docker volumes (shared ones are split evenly)
	Binds   int64 // bind-mounted directories (shared ones are split evenly)
	Logs    int64 // container log files

	VolumesUnknown bool
	BindsUnknown   bool
	LogsUnknown    bool
}

// Total is the sum of all parts.
func (p DiskParts) Total() int64 { return p.Layers + p.Volumes + p.Binds + p.Logs }

// Complete reports whether every part was measured completely.
func (p DiskParts) Complete() bool { return !p.VolumesUnknown && !p.BindsUnknown && !p.LogsUnknown }

func (p *DiskParts) add(o DiskParts) {
	p.Layers += o.Layers
	p.Volumes += o.Volumes
	p.Binds += o.Binds
	p.Logs += o.Logs
	p.VolumesUnknown = p.VolumesUnknown || o.VolumesUnknown
	p.BindsUnknown = p.BindsUnknown || o.BindsUnknown
	p.LogsUnknown = p.LogsUnknown || o.LogsUnknown
}

// BindShare is a bind-mounted directory and its size.
type BindShare struct {
	Path  string
	Bytes int64
	Known bool
}

// ProjectDisk is the disk use of a compose project, or of one standalone
// container (Standalone is set and Name is the container name).
type ProjectDisk struct {
	Name       string
	Standalone bool
	Containers []string // container IDs
	DiskParts
}

// DiskUsage is the result of one background refresh of the Docker disk usage.
// It is never changed after it was created, so snapshots can share it.
type DiskUsage struct {
	At      time.Time
	RootDir string // Docker's data directory, e.g. /var/lib/docker

	ByContainer map[string]DiskParts // by container ID
	Projects    []ProjectDisk        // biggest first

	Images      int64 // all images (shared layers once)
	BuildCache  int64
	Volumes     int64       // all volumes, used or not
	Binds       []BindShare // each directory once
	BindsTotal  int64
	Layers      int64 // writable layers of all containers
	Logs        int64
	LogsUnknown bool

	UnusedVolumes int64 // volumes no container mounts
	DanglingImg   int64 // untagged images no container uses
	Reclaimable   int64 // UnusedVolumes + DanglingImg

	DockerTotal int64 // everything Docker accounts for, binds included
	Unknown     bool  // some part could not be measured completely
}

// DiskContainer is one container as the aggregation sees it.
type DiskContainer struct {
	ID, Name, Project string
	SizeRw            int64
	LogBytes          int64
	LogKnown          bool
	Volumes           []string // names of volumes it mounts
	Binds             []string // bind-mounted directories (already filtered)
}

// diskInput is everything aggregateDisk needs. It has no Docker types, so the
// aggregation can be tested with plain numbers.
type diskInput struct {
	RootDir    string
	Containers []DiskContainer
	Volumes    map[string]sizeInfo // all volumes by name
	Binds      map[string]sizeInfo // by effective path (see collapseBinds)
	Images     int64
	Dangling   int64
	BuildCache int64
}

// systemBindPrefixes are never counted as bind-mount data: they are system
// directories, not project data (or are not regular disk use at all).
var systemBindPrefixes = []string{"/etc", "/proc", "/sys", "/dev", "/run", "/var/run", "/var/lib/docker"}

// skipBindPath reports whether a bind-mount source is left out: the root
// itself, and the system directories above including everything below them.
func skipBindPath(p string) bool {
	p = path.Clean(p)
	if p == "/" || p == "." {
		return true
	}
	for _, s := range systemBindPrefixes {
		if p == s || strings.HasPrefix(p, s+"/") {
			return true
		}
	}
	return false
}

// isUnder reports whether p lies inside dir (and is not dir itself).
func isUnder(p, dir string) bool { return p != dir && strings.HasPrefix(p, dir+"/") }

// collapseBinds maps every bind path of a container to its effective path: a
// path inside another bind path is part of the outer directory, whose size
// already contains it. This keeps nested paths from being counted twice.
// It returns the effective paths per container ID and the unique effective paths.
func collapseBinds(perContainer map[string][]string) (eff map[string][]string, unique []string) {
	all := map[string]bool{}
	for _, paths := range perContainer {
		for _, p := range paths {
			all[path.Clean(p)] = true
		}
	}
	outer := func(p string) string {
		best := p
		for q := range all {
			if isUnder(p, q) && len(q) < len(best) {
				best = q
			}
		}
		return best
	}

	eff = make(map[string][]string, len(perContainer))
	seen := map[string]bool{}
	for id, paths := range perContainer {
		mine := map[string]bool{}
		for _, p := range paths {
			o := outer(path.Clean(p))
			if !mine[o] {
				mine[o] = true
				eff[id] = append(eff[id], o)
			}
			if !seen[o] {
				seen[o] = true
				unique = append(unique, o)
			}
		}
		sort.Strings(eff[id])
	}
	sort.Strings(unique)
	return eff, unique
}

// aggregateDisk splits the disk use among containers and projects.
// A volume or bind directory that several containers use is split evenly among
// them, so the project totals add up to the real total.
func aggregateDisk(in diskInput, at time.Time) *DiskUsage {
	out := &DiskUsage{At: at, RootDir: in.RootDir, ByContainer: map[string]DiskParts{}, Images: in.Images, BuildCache: in.BuildCache, DanglingImg: in.Dangling}

	binds := map[string][]string{}
	for _, c := range in.Containers {
		binds[c.ID] = c.Binds
	}
	effBinds, uniqueBinds := collapseBinds(binds)

	// Who uses what.
	volOwners := map[string]int{}
	bindOwners := map[string]int{}
	for _, c := range in.Containers {
		seen := map[string]bool{}
		for _, v := range c.Volumes {
			if !seen[v] {
				seen[v] = true
				volOwners[v]++
			}
		}
		for _, b := range effBinds[c.ID] {
			bindOwners[b]++
		}
	}

	for _, c := range in.Containers {
		var p DiskParts
		p.Layers = max(c.SizeRw, 0)
		p.Logs, p.LogsUnknown = c.LogBytes, !c.LogKnown
		seen := map[string]bool{}
		for _, v := range c.Volumes {
			if seen[v] {
				continue
			}
			seen[v] = true
			vi, ok := in.Volumes[v]
			if !ok {
				p.VolumesUnknown = true
				continue
			}
			p.Volumes += vi.Bytes / int64(volOwners[v])
			p.VolumesUnknown = p.VolumesUnknown || !vi.Known
		}
		for _, b := range effBinds[c.ID] {
			bi := in.Binds[b]
			p.Binds += bi.Bytes / int64(bindOwners[b])
			p.BindsUnknown = p.BindsUnknown || !bi.Known
		}
		out.ByContainer[c.ID] = p
		out.Layers += p.Layers
		out.Logs += p.Logs
		out.LogsUnknown = out.LogsUnknown || p.LogsUnknown
	}

	// Volumes: all of them count for the total, unused ones are reclaimable.
	for name, vi := range in.Volumes {
		out.Volumes += vi.Bytes
		if volOwners[name] == 0 {
			out.UnusedVolumes += vi.Bytes
		}
		out.Unknown = out.Unknown || !vi.Known
	}
	for _, b := range uniqueBinds {
		bi := in.Binds[b]
		out.Binds = append(out.Binds, BindShare{Path: b, Bytes: bi.Bytes, Known: bi.Known})
		out.BindsTotal += bi.Bytes
		out.Unknown = out.Unknown || !bi.Known
	}
	out.Unknown = out.Unknown || out.LogsUnknown
	out.Reclaimable = out.UnusedVolumes + out.DanglingImg
	out.DockerTotal = out.Layers + out.Logs + out.Volumes + out.BindsTotal + out.Images + out.BuildCache

	// Projects: compose projects as one entry, standalone containers each alone.
	byKey := map[string]*ProjectDisk{}
	var order []*ProjectDisk
	for _, c := range in.Containers {
		key, name, standalone := "project:"+c.Project, c.Project, false
		if c.Project == "" {
			key, name, standalone = "container:"+c.ID, c.Name, true
		}
		pd, ok := byKey[key]
		if !ok {
			pd = &ProjectDisk{Name: name, Standalone: standalone}
			byKey[key] = pd
			order = append(order, pd)
		}
		pd.Containers = append(pd.Containers, c.ID)
		pd.add(out.ByContainer[c.ID])
	}
	for _, pd := range order {
		out.Projects = append(out.Projects, *pd)
	}
	sort.SliceStable(out.Projects, func(i, j int) bool {
		a, b := out.Projects[i], out.Projects[j]
		if a.Total() != b.Total() {
			return a.Total() > b.Total()
		}
		return a.Name < b.Name
	})
	return out
}

// ProjectOf returns the project entry a container belongs to, or nil.
func (u *DiskUsage) ProjectOf(id string) *ProjectDisk {
	for i := range u.Projects {
		for _, c := range u.Projects[i].Containers {
			if c == id {
				return &u.Projects[i]
			}
		}
	}
	return nil
}

// MountFor returns the mount a path lives on: the one with the longest path
// that is a prefix of it. Nil if no mount matches.
func MountFor(mounts []Mount, p string) *Mount {
	p = path.Clean(p)
	var best *Mount
	for i := range mounts {
		m := &mounts[i]
		if p == m.Path || m.Path == "/" || strings.HasPrefix(p, m.Path+"/") {
			if best == nil || len(m.Path) > len(best.Path) {
				best = m
			}
		}
	}
	return best
}

// --- Fetching ----------------------------------------------------------------

// diskUsage asks Docker for the numbers and measures logs and bind mounts. It
// only reads. It is slow (seconds, with big bind directories much longer), so
// it must only run in the background.
func (d *Docker) diskUsage(ctx context.Context) (*DiskUsage, error) {
	if d.err != nil {
		return nil, d.err
	}
	res, err := d.cli.DiskUsage(ctx, client.DiskUsageOptions{
		Containers: true, Images: true, BuildCache: true, Volumes: true, Verbose: true,
	})
	if err != nil {
		return nil, err
	}
	rootDir := ""
	if info, err := d.cli.Info(ctx, client.InfoOptions{}); err == nil {
		rootDir = info.Info.DockerRootDir
	}

	in := diskInput{
		RootDir:    rootDir,
		Volumes:    map[string]sizeInfo{},
		Binds:      map[string]sizeInfo{},
		Images:     res.Images.TotalSize,
		BuildCache: res.BuildCache.TotalSize,
		Dangling:   danglingBytes(res),
	}
	for _, v := range res.Volumes.Items {
		si := sizeInfo{}
		if v.UsageData != nil && v.UsageData.Size >= 0 {
			si = sizeInfo{Bytes: v.UsageData.Size, Known: true}
		}
		in.Volumes[v.Name] = si
	}

	binds := map[string][]string{}
	for _, s := range res.Containers.Items {
		c := DiskContainer{ID: s.ID, Name: containerName(s), Project: s.Labels[labelProject], SizeRw: s.SizeRw}
		mounts := s.Mounts
		if info, err := d.cli.ContainerInspect(ctx, s.ID, client.ContainerInspectOptions{}); err == nil {
			mounts = info.Container.Mounts
			c.LogBytes, c.LogKnown = logSize(info.Container.LogPath)
		}
		for _, m := range mounts {
			switch string(m.Type) {
			case "volume":
				c.Volumes = append(c.Volumes, m.Name)
			case "bind":
				if p := bindSource(m.Source); p != "" {
					c.Binds = append(c.Binds, p)
				}
			}
		}
		binds[c.ID] = c.Binds
		in.Containers = append(in.Containers, c)
	}

	_, unique := collapseBinds(binds)
	for _, p := range unique {
		in.Binds[p] = dirSize(ctx, p)
	}
	return aggregateDisk(in, time.Now()), nil
}

func containerName(s container.Summary) string {
	if len(s.Names) > 0 {
		return strings.TrimPrefix(s.Names[0], "/")
	}
	return s.ID[:min(len(s.ID), 12)]
}

// danglingBytes sums the images without a tag that no container uses. Layers
// shared with other images do not count, as removing the image would keep them.
func danglingBytes(res client.DiskUsageResult) int64 {
	var sum int64
	for _, img := range res.Images.Items {
		if len(img.RepoTags) > 0 || img.Containers > 0 {
			continue
		}
		size := img.Size
		if img.SharedSize > 0 {
			size -= img.SharedSize
		}
		sum += max(size, 0)
	}
	return sum
}

// bindSource returns the source directory of a bind mount, or "" if it should
// not be counted: system paths, sockets, single files, paths that do not exist.
// A path that exists but cannot be examined is kept (its size will be unknown).
func bindSource(p string) string {
	if p == "" || skipBindPath(p) {
		return ""
	}
	info, err := os.Stat(p)
	switch {
	case err == nil && !info.IsDir():
		return ""
	case err != nil && errors.Is(err, fs.ErrNotExist):
		return ""
	}
	return path.Clean(p)
}

// logSize returns the size of a container's log file and its rotated files. The
// size is unknown if the file cannot be examined (for example a normal user has
// no access to /var/lib/docker), or if the container has no log file path.
func logSize(logPath string) (bytes int64, known bool) {
	if logPath == "" {
		return 0, false
	}
	info, err := os.Stat(logPath)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, true // never wrote a log
	}
	if err != nil {
		return 0, false
	}
	bytes = info.Size()
	rotated, _ := filepath.Glob(logPath + ".*")
	for _, f := range rotated {
		if fi, err := os.Stat(f); err == nil {
			bytes += fi.Size()
		}
	}
	return bytes, true
}

// dirSize walks a directory and adds up the disk space of everything in it, like
// `du`. Symbolic links are not followed and other filesystems mounted inside
// are not entered. Parts that cannot be read, or running into the deadline,
// make the result "not known" but keep what was measured.
func dirSize(ctx context.Context, root string) sizeInfo {
	var rootDev uint64
	if fi, err := os.Stat(root); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			rootDev = uint64(st.Dev)
		}
	} else {
		return sizeInfo{}
	}

	res := sizeInfo{Known: true}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			res.Known = false
			return filepath.SkipAll
		}
		if err != nil {
			res.Known = false
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			res.Known = false
			return nil
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			res.Bytes += info.Size()
			return nil
		}
		if d.IsDir() && p != root && uint64(st.Dev) != rootDev {
			return fs.SkipDir // another filesystem is mounted here
		}
		res.Bytes += st.Blocks * 512
		return nil
	})
	return res
}
