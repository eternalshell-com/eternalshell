package collector

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Kind says where a process comes from.
type Kind int

const (
	KindHost      Kind = iota // not in a container, not in a systemd service
	KindService               // runs in a systemd service
	KindContainer             // runs in a Docker container
)

// Origin describes which container or service a process belongs to.
type Origin struct {
	Kind        Kind
	ContainerID string // full 64-char ID, KindContainer only
	Unit        string // e.g. "ssh.service", KindService only
	CgroupPath  string // cgroup of the container, relative to /sys/fs/cgroup

	// Filled in from the Docker API when it is reachable.
	ContainerName string
	Project       string // compose project, empty if standalone
	Service       string // compose service
	Image         string // short image name
}

// containerRe finds a Docker container ID in a cgroup path. It matches both the
// systemd driver ("docker-<id>.scope") and the cgroupfs driver ("/docker/<id>").
var containerRe = regexp.MustCompile(`(?:docker-|/docker/)([0-9a-f]{64})`)

// ParseCgroup turns the content of /proc/<pid>/cgroup into an Origin.
// It is a pure function (text in, struct out) so it can be tested without a system.
func ParseCgroup(content string) Origin {
	var fallbackPath string
	for _, line := range strings.Split(content, "\n") {
		// Each line is "hierarchy-ID:controllers:path".
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		path := parts[2]
		if m := containerRe.FindStringSubmatchIndex(path); m != nil {
			// Cut the path after the container's own cgroup, so processes in
			// nested cgroups still resolve to the container's memory.current.
			end := m[1]
			if i := strings.IndexByte(path[end:], '/'); i >= 0 {
				end += i
			} else {
				end = len(path)
			}
			return Origin{
				Kind:        KindContainer,
				ContainerID: path[m[2]:m[3]],
				CgroupPath:  path[:end],
			}
		}
		if parts[0] == "0" || fallbackPath == "" {
			fallbackPath = path // cgroup v2 line, or the first v1 line as fallback
		}
	}

	// Walk the path from the end and take the innermost *.service.
	segs := strings.Split(fallbackPath, "/")
	for i := len(segs) - 1; i >= 0; i-- {
		if strings.HasSuffix(segs[i], ".service") {
			return Origin{Kind: KindService, Unit: segs[i]}
		}
	}
	return Origin{Kind: KindHost}
}

// readOrigin reads and parses /proc/<pid>/cgroup. Any error means "host":
// a process that vanished or cannot be read must not break the snapshot.
func readOrigin(pid int32) Origin {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return Origin{Kind: KindHost}
	}
	return ParseCgroup(string(b))
}

// String is the text shown in the ORIGIN column.
func (o Origin) String() string {
	switch o.Kind {
	case KindContainer:
		switch {
		case o.Project != "" && o.Service != "":
			return fmt.Sprintf("container %s/%s", o.Project, o.Service)
		case o.ContainerName != "" && o.Image != "":
			return fmt.Sprintf("container %s (%s)", o.ContainerName, o.Image)
		case o.ContainerName != "":
			return "container " + o.ContainerName
		default:
			return "container " + o.ContainerID[:12]
		}
	case KindService:
		return "systemd: " + o.Unit
	default:
		return "host"
	}
}
