package collector

import (
	"context"
	"errors"
	"fmt"
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
	// walking the project folders and bind-mount directories.
	diskTimeout = 2 * time.Minute

	// folderEvery is how long a measured folder size is reused. Walking folders
	// is the expensive part, so it happens far less often than the Docker calls.
	folderEvery = 30 * time.Minute

	labelWorkDir = "com.docker.compose.project.working_dir"
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
	Files   int64 // the compose project folder (projects only, not single containers)
	Binds   int64 // bind-mounted directories outside project folders (split evenly)
	Logs    int64 // container log files

	VolumesUnknown bool
	FilesUnknown   bool
	BindsUnknown   bool
	LogsUnknown    bool
}

// Total is the sum of all parts.
func (p DiskParts) Total() int64 { return p.Layers + p.Volumes + p.Files + p.Binds + p.Logs }

// Complete reports whether every part was measured completely.
func (p DiskParts) Complete() bool {
	return !p.VolumesUnknown && !p.FilesUnknown && !p.BindsUnknown && !p.LogsUnknown
}

func (p *DiskParts) add(o DiskParts) {
	p.Layers += o.Layers
	p.Volumes += o.Volumes
	p.Files += o.Files
	p.Binds += o.Binds
	p.Logs += o.Logs
	p.VolumesUnknown = p.VolumesUnknown || o.VolumesUnknown
	p.FilesUnknown = p.FilesUnknown || o.FilesUnknown
	p.BindsUnknown = p.BindsUnknown || o.BindsUnknown
	p.LogsUnknown = p.LogsUnknown || o.LogsUnknown
}

// BindShare is a bind-mounted directory and its size (without any folder
// counted inside it).
type BindShare struct {
	Path  string
	Bytes int64
	Known bool
}

// FolderShare is a compose project folder and its size (without any folder
// counted inside it). Projects are its owners.
type FolderShare struct {
	Path     string
	Bytes    int64
	Known    bool
	Projects []string
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
	Volumes     int64         // all volumes, used or not
	Folders     []FolderShare // compose project folders, each byte once
	FilesTotal  int64
	Binds       []BindShare // bind directories outside project folders
	BindsTotal  int64
	Layers      int64 // writable layers of all containers
	Logs        int64
	LogsUnknown bool

	// FoldersMeasured is when the oldest folder size was measured; folder sizes
	// are reused for up to folderEvery.
	FoldersMeasured time.Time

	// Notes are things worth a log line, e.g. a project folder that was skipped.
	Notes []string

	UnusedVolumes int64 // volumes no container mounts
	DanglingImg   int64 // untagged images no container uses
	Reclaimable   int64 // UnusedVolumes + DanglingImg

	DockerTotal int64 // everything Docker accounts for, binds included
	Unknown     bool  // some part could not be measured completely
}

// DiskContainer is one container as the aggregation sees it.
type DiskContainer struct {
	ID, Name, Project string
	WorkDir           string // compose project folder, already checked; "" if none
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
	Sizes      map[string]sizeInfo // full size of every root from planRoots (nested ones included)
	Images     int64
	Dangling   int64
	BuildCache int64
	Notes      []string
	Measured   time.Time
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

// rootPlan lists what has to be measured and who owns it.
type rootPlan struct {
	Folders map[string][]string // project folder -> project names (sorted)
	Binds   map[string][]string // bind directory outside every project folder -> container IDs (sorted)
	Roots   []string            // all of them, sorted
}

// planRoots decides which directories are counted. Roots are the project
// folders and the bind directories outside them. A bind inside (or equal to) a
// project folder is not a root: its bytes are already in the project's files.
func planRoots(cs []DiskContainer) rootPlan {
	plan := rootPlan{Folders: map[string][]string{}, Binds: map[string][]string{}}

	owners := map[string]map[string]bool{}
	for _, c := range cs {
		if c.Project == "" || c.WorkDir == "" {
			continue
		}
		dir := path.Clean(c.WorkDir)
		if owners[dir] == nil {
			owners[dir] = map[string]bool{}
		}
		owners[dir][c.Project] = true
	}
	for dir, set := range owners {
		plan.Folders[dir] = sortedKeys(set)
	}

	insideFolder := func(p string) bool {
		for f := range plan.Folders {
			if p == f || isUnder(p, f) {
				return true
			}
		}
		return false
	}
	bindOwners := map[string]map[string]bool{}
	for _, c := range cs {
		for _, b := range c.Binds {
			b = path.Clean(b)
			if insideFolder(b) {
				continue
			}
			if bindOwners[b] == nil {
				bindOwners[b] = map[string]bool{}
			}
			bindOwners[b][c.ID] = true
		}
	}
	for b, set := range bindOwners {
		plan.Binds[b] = sortedKeys(set)
	}

	for f := range plan.Folders {
		plan.Roots = append(plan.Roots, f)
	}
	for b := range plan.Binds {
		plan.Roots = append(plan.Roots, b)
	}
	sort.Strings(plan.Roots)
	return plan
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// exclusiveSizes turns full sizes into exclusive ones: every root loses the
// full sizes of the roots directly inside it, so each byte is counted once and
// belongs to the innermost root. If a nested root is not fully known, the outer
// one is marked unknown too, because what to subtract is uncertain.
func exclusiveSizes(full map[string]sizeInfo) map[string]sizeInfo {
	roots := make([]string, 0, len(full))
	for r := range full {
		roots = append(roots, r)
	}
	// parent = the longest other root that contains it
	parent := map[string]string{}
	for _, r := range roots {
		for _, q := range roots {
			if isUnder(r, q) && len(q) > len(parent[r]) {
				parent[r] = q
			}
		}
	}

	out := make(map[string]sizeInfo, len(full))
	for r, si := range full {
		out[r] = si
	}
	for child, par := range parent {
		o := out[par]
		o.Bytes -= full[child].Bytes
		o.Known = o.Known && full[child].Known
		out[par] = o
	}
	for r, si := range out {
		si.Bytes = max(si.Bytes, 0)
		out[r] = si
	}
	return out
}

// aggregateDisk splits the disk use among containers and projects.
// A volume that several containers use, a bind directory, or a project folder
// shared by project names is split evenly among its owners, so the totals add
// up to the real size. Every byte of a folder is counted once (see exclusiveSizes).
func aggregateDisk(in diskInput, at time.Time) *DiskUsage {
	out := &DiskUsage{
		At: at, RootDir: in.RootDir, ByContainer: map[string]DiskParts{},
		Images: in.Images, BuildCache: in.BuildCache, DanglingImg: in.Dangling,
		Notes: in.Notes, FoldersMeasured: in.Measured,
	}

	plan := planRoots(in.Containers)
	full := make(map[string]sizeInfo, len(plan.Roots))
	for _, r := range plan.Roots {
		full[r] = in.Sizes[r] // a root with no entry stays unknown, with 0 bytes
	}
	excl := exclusiveSizes(full)

	// Volumes: who uses what.
	volOwners := map[string]int{}
	for _, c := range in.Containers {
		seen := map[string]bool{}
		for _, v := range c.Volumes {
			if !seen[v] {
				seen[v] = true
				volOwners[v]++
			}
		}
	}

	// Bind directories per container (only those that are roots).
	containerBinds := map[string][]string{}
	for b, ids := range plan.Binds {
		for _, id := range ids {
			containerBinds[id] = append(containerBinds[id], b)
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
		for _, b := range containerBinds[c.ID] {
			bi := excl[b]
			p.Binds += bi.Bytes / int64(len(plan.Binds[b]))
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

	// Project folders and bind directories, each byte once.
	projFiles := map[string]DiskParts{}
	for _, f := range sortedKeys(setOf(plan.Folders)) {
		fi, owners := excl[f], plan.Folders[f]
		out.Folders = append(out.Folders, FolderShare{Path: f, Bytes: fi.Bytes, Known: fi.Known, Projects: owners})
		out.FilesTotal += fi.Bytes
		out.Unknown = out.Unknown || !fi.Known
		for _, name := range owners {
			pf := projFiles[name]
			pf.Files += fi.Bytes / int64(len(owners))
			pf.FilesUnknown = pf.FilesUnknown || !fi.Known
			projFiles[name] = pf
		}
	}
	for _, b := range sortedKeys(setOf(plan.Binds)) {
		bi := excl[b]
		out.Binds = append(out.Binds, BindShare{Path: b, Bytes: bi.Bytes, Known: bi.Known})
		out.BindsTotal += bi.Bytes
		out.Unknown = out.Unknown || !bi.Known
	}
	out.Unknown = out.Unknown || out.LogsUnknown
	out.Reclaimable = out.UnusedVolumes + out.DanglingImg
	out.DockerTotal = out.Layers + out.Logs + out.Volumes + out.FilesTotal + out.BindsTotal + out.Images + out.BuildCache

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
			if !standalone {
				pd.add(projFiles[name]) // the folder belongs to the project once
			}
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

// setOf returns the keys of a string-keyed map as a set, for sortedKeys.
func setOf[V any](m map[string]V) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

// FilesOf returns the size of a compose project's folder. Unknown projects
// (and standalone containers) have none.
func (u *DiskUsage) FilesOf(project string) (bytes int64, unknown bool) {
	for _, p := range u.Projects {
		if !p.Standalone && p.Name == project {
			return p.Files, p.FilesUnknown
		}
	}
	return 0, false
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

// sizeCache remembers folder sizes so the expensive directory walks happen at
// most once per `every`. It is used by one refresh at a time (the refreshes never
// overlap), so it needs no lock of its own.
type sizeCache struct {
	every   time.Duration
	entries map[string]cachedSize
}

type cachedSize struct {
	size sizeInfo
	at   time.Time
}

func newSizeCache(every time.Duration) *sizeCache {
	return &sizeCache{every: every, entries: map[string]cachedSize{}}
}

// sizes returns the full size of every root and when the oldest of them was
// measured. walk is only called for roots that are new or older than `every`.
//
// A walk that did not finish (timeout, unreadable parts) is not stored as fresh,
// so the next refresh tries again. An older complete value is shown instead of
// the partial one: a stale number beats "+?". Roots that are gone are forgotten.
func (c *sizeCache) sizes(roots []string, now time.Time, walk func(root string) sizeInfo) (map[string]sizeInfo, time.Time) {
	out := make(map[string]sizeInfo, len(roots))
	oldest := now
	keep := make(map[string]bool, len(roots))
	for _, r := range roots {
		keep[r] = true
		e, have := c.entries[r]
		if have && e.size.Known && now.Sub(e.at) < c.every {
			out[r] = e.size
			oldest = minTime(oldest, e.at)
			continue
		}
		got := walk(r)
		switch {
		case got.Known:
			c.entries[r] = cachedSize{size: got, at: now}
			out[r] = got
		case have && e.size.Known:
			out[r] = e.size // stale but complete
			oldest = minTime(oldest, e.at)
		default:
			out[r] = got
		}
	}
	for r := range c.entries {
		if !keep[r] {
			delete(c.entries, r)
		}
	}
	return out, oldest
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// diskUsage asks Docker for the numbers and measures logs, project folders and
// bind mounts. It only reads. It is slow (seconds; walking big folders much
// longer), so it must only run in the background. Folder sizes come from the
// cache and are only walked again every folderEvery.
func (d *Docker) diskUsage(ctx context.Context, cache *sizeCache) (*DiskUsage, error) {
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

	noted := map[string]bool{}
	for _, s := range res.Containers.Items {
		c := DiskContainer{ID: s.ID, Name: containerName(s), Project: s.Labels[labelProject], SizeRw: s.SizeRw}
		workDir := s.Labels[labelWorkDir]
		mounts := s.Mounts
		if info, err := d.cli.ContainerInspect(ctx, s.ID, client.ContainerInspectOptions{}); err == nil {
			mounts = info.Container.Mounts
			c.LogBytes, c.LogKnown = logSize(info.Container.LogPath)
			if cfg := info.Container.Config; cfg != nil { // the labels of the container itself
				if v := cfg.Labels[labelProject]; v != "" {
					c.Project = v
				}
				if v := cfg.Labels[labelWorkDir]; v != "" {
					workDir = v
				}
			}
		}
		if c.Project != "" && workDir != "" {
			dir, note := folderSource(workDir)
			c.WorkDir = dir
			if note != "" && !noted[workDir] {
				noted[workDir] = true
				in.Notes = append(in.Notes, fmt.Sprintf("project folder %s of project %s skipped: %s", workDir, c.Project, note))
			}
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
		in.Containers = append(in.Containers, c)
	}
	sort.Strings(in.Notes)

	plan := planRoots(in.Containers)
	rootSet := make(map[string]bool, len(plan.Roots))
	for _, r := range plan.Roots {
		rootSet[r] = true
	}
	in.Sizes, in.Measured = cache.sizes(plan.Roots, time.Now(), func(root string) sizeInfo {
		return dirSize(ctx, root, rootSet)
	})
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

// folderSource checks a compose project folder. It returns the cleaned path, or
// "" and the reason if the folder is not counted: a system path or "/", missing,
// or not a directory. A folder that exists but cannot be examined is kept; its
// size will then be marked unknown.
func folderSource(p string) (dir, note string) {
	if skipBindPath(p) {
		return "", "system path"
	}
	info, err := os.Stat(p)
	switch {
	case err == nil && !info.IsDir():
		return "", "not a directory"
	case err != nil && errors.Is(err, fs.ErrNotExist):
		return "", "does not exist on this machine"
	}
	return path.Clean(p), ""
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
// are not entered, except a directory that is itself one of the counted roots
// (its size is subtracted again by exclusiveSizes). Parts that cannot be read, or running into the deadline,
// make the result "not known" but keep what was measured.
func dirSize(ctx context.Context, root string, roots map[string]bool) sizeInfo {
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
		if d.IsDir() && p != root && uint64(st.Dev) != rootDev && !roots[p] {
			return fs.SkipDir // another filesystem is mounted here (unless it is a root itself)
		}
		res.Bytes += st.Blocks * 512
		return nil
	})
	return res
}
