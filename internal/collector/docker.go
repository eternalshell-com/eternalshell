package collector

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

const (
	// cgroupBase is where the cgroup v2 hierarchy is mounted.
	cgroupBase = "/sys/fs/cgroup"

	// restartTTL is how long a cached restart count is trusted. The count is only
	// available from the (more expensive) inspect call, so we do not ask every tick.
	restartTTL = 30 * time.Second

	dockerTimeout = 3 * time.Second

	labelProject = "com.docker.compose.project"
	labelService = "com.docker.compose.service"
)

// Container is one Docker container. Name, project, state and restarts come from
// the Docker API (metadata only); CPU and memory come from /proc and the cgroup.
type Container struct {
	ID       string
	Name     string
	Project  string // compose project, empty if standalone
	Service  string // compose service
	State    string // running, exited, restarting, ...
	Status   string // human text from Docker, e.g. "Up 2 weeks (healthy)"
	Restarts int

	CPU        float64 // sum of its processes' CPU, 100 = one core
	MemBytes   uint64  // cgroup memory.current minus inactive_file
	MemOK      bool    // false if the cgroup could not be read (e.g. stopped)
	CgroupPath string  // taken from the container's processes
}

// Running reports whether the container is up.
func (c Container) Running() bool { return c.State == "running" }

type restartInfo struct {
	state    string
	at       time.Time
	restarts int
}

// Docker reads container metadata through the Docker API. It only makes
// read-only calls (list and inspect) and never talks to the stats endpoint:
// stats are expensive for dockerd when there are many containers.
type Docker struct {
	cli   *client.Client
	err   error // set if the client could not even be created
	cache map[string]restartInfo
}

func newDocker() *Docker {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	return &Docker{cli: cli, err: err, cache: map[string]restartInfo{}}
}

func (d *Docker) close() {
	if d.cli != nil {
		d.cli.Close()
	}
}

// list returns all containers (running or not), sorted by project and name.
func (d *Docker) list(ctx context.Context) ([]Container, error) {
	if d.err != nil {
		return nil, d.err
	}
	summaries, err := d.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}

	now := time.Now()
	seen := make(map[string]bool, len(summaries))
	out := make([]Container, 0, len(summaries))
	for _, s := range summaries {
		c := fromSummary(s)
		c.Restarts = d.restarts(ctx, c, now)
		seen[c.ID] = true
		out = append(out, c)
	}
	for id := range d.cache { // forget containers that no longer exist
		if !seen[id] {
			delete(d.cache, id)
		}
	}
	sortContainers(out)
	return out, nil
}

// restarts returns the restart count, using the cache while it is fresh and the
// container's state has not changed.
func (d *Docker) restarts(ctx context.Context, c Container, now time.Time) int {
	e, ok := d.cache[c.ID]
	if ok && e.state == c.State && now.Sub(e.at) < restartTTL {
		return e.restarts
	}
	info, err := d.cli.ContainerInspect(ctx, c.ID)
	if err != nil || info.ContainerJSONBase == nil {
		return e.restarts // zero if never known
	}
	d.cache[c.ID] = restartInfo{state: c.State, at: now, restarts: info.RestartCount}
	return info.RestartCount
}

// fromSummary converts the Docker API's list entry to our Container.
func fromSummary(s container.Summary) Container {
	name := s.ID[:12]
	if len(s.Names) > 0 {
		name = strings.TrimPrefix(s.Names[0], "/")
	}
	return Container{
		ID:      s.ID,
		Name:    name,
		Project: s.Labels[labelProject],
		Service: s.Labels[labelService],
		State:   string(s.State),
		Status:  s.Status,
	}
}

// sortContainers orders by project (standalone containers last), then name.
func sortContainers(cs []Container) {
	sort.Slice(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if (a.Project == "") != (b.Project == "") {
			return b.Project == ""
		}
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		return a.Name < b.Name
	})
}

// attach connects processes and containers, in place:
//   - each container process gets the container's name, project and service
//   - each container gets CPU = sum of its processes' CPU, and its cgroup path
func attach(procs []Process, cs []Container) {
	byID := make(map[string]int, len(cs))
	for i, c := range cs {
		byID[c.ID] = i
	}
	for i := range procs {
		o := &procs[i].Origin
		if o.Kind != KindContainer {
			continue
		}
		idx, ok := byID[o.ContainerID]
		if !ok {
			continue // Docker does not know it (or is unreachable): keep the short ID
		}
		c := &cs[idx]
		o.ContainerName, o.Project, o.Service = c.Name, c.Project, c.Service
		c.CPU += procs[i].CPU
		if c.CgroupPath == "" {
			c.CgroupPath = o.CgroupPath
		}
	}
}

// readMemUsage returns the container's memory like `docker stats` does:
// memory.current minus inactive_file (reclaimable page cache).
// ok is false if memory.current cannot be read.
func readMemUsage(base, cgroupPath string) (usage uint64, ok bool) {
	dir := filepath.Join(base, cgroupPath)
	b, err := os.ReadFile(filepath.Join(dir, "memory.current"))
	if err != nil {
		return 0, false
	}
	cur, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0, false
	}
	if stat, err := os.ReadFile(filepath.Join(dir, "memory.stat")); err == nil {
		if inactive, found := statValue(string(stat), "inactive_file"); found && inactive < cur {
			cur -= inactive
		}
	}
	return cur, true
}

// statValue finds "<key> <number>" in the content of a memory.stat file.
func statValue(stat, key string) (uint64, bool) {
	for _, line := range strings.Split(stat, "\n") {
		k, v, found := strings.Cut(line, " ")
		if !found || k != key {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		return n, err == nil
	}
	return 0, false
}
