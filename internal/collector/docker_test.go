package collector

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/moby/api/types/container"
)

func TestFromSummary(t *testing.T) {
	s := container.Summary{
		ID:     id,
		Names:  []string{"/lienks-app-clickhouse-1"},
		Labels: map[string]string{labelProject: "lienks-app", labelService: "clickhouse"},
		State:  "running",
		Status: "Up 7 weeks (healthy)",
	}
	c := fromSummary(s)
	if c.Name != "lienks-app-clickhouse-1" || c.Project != "lienks-app" || c.Service != "clickhouse" || c.State != "running" {
		t.Errorf("unexpected container: %+v", c)
	}

	standalone := fromSummary(container.Summary{ID: id, Names: []string{"/traefik"}})
	if standalone.Project != "" || standalone.Name != "traefik" {
		t.Errorf("unexpected standalone container: %+v", standalone)
	}
}

func TestSortContainers(t *testing.T) {
	cs := []Container{
		{Name: "traefik"},
		{Name: "web", Project: "istbarrierefrei"},
		{Name: "db", Project: "lienks-app"},
		{Name: "app", Project: "lienks-app"},
		{Name: "kuma"},
	}
	sortContainers(cs)
	want := []string{"web", "app", "db", "kuma", "traefik"}
	for i, n := range want {
		if cs[i].Name != n {
			t.Fatalf("order[%d] = %q, want %q (all: %+v)", i, cs[i].Name, n, cs)
		}
	}
}

func TestAttach(t *testing.T) {
	other := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	procs := []Process{
		{PID: 1, CPU: 120, Origin: Origin{Kind: KindContainer, ContainerID: id, CgroupPath: "/system.slice/docker-" + id + ".scope"}},
		{PID: 2, CPU: 30, Origin: Origin{Kind: KindContainer, ContainerID: id, CgroupPath: "/system.slice/docker-" + id + ".scope"}},
		{PID: 3, CPU: 50, Origin: Origin{Kind: KindHost}},
		{PID: 4, CPU: 10, Origin: Origin{Kind: KindContainer, ContainerID: other}}, // unknown to Docker
	}
	cs := []Container{{ID: id, Name: "clickhouse-1", Project: "lienks-app", Service: "clickhouse", Image: "clickhouse/clickhouse-server"}}

	attach(procs, cs)

	if cs[0].CPU != 150 {
		t.Errorf("container CPU = %v, want 150", cs[0].CPU)
	}
	if cs[0].CgroupPath == "" {
		t.Error("container CgroupPath not set")
	}
	if got := procs[0].Origin.String(); got != "container lienks-app/clickhouse" {
		t.Errorf("origin = %q", got)
	}
	if procs[0].Origin.Image != "clickhouse/clickhouse-server" {
		t.Errorf("image not copied to origin: %+v", procs[0].Origin)
	}
	if got := procs[3].Origin.String(); got != "container ffffffffffff" {
		t.Errorf("unknown container origin = %q", got)
	}
}

func writeCgroup(t *testing.T, current, stat string) (base, path string) {
	t.Helper()
	base = t.TempDir()
	path = "/system.slice/docker-x.scope"
	dir := filepath.Join(base, path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if current != "" {
		os.WriteFile(filepath.Join(dir, "memory.current"), []byte(current), 0o644)
	}
	if stat != "" {
		os.WriteFile(filepath.Join(dir, "memory.stat"), []byte(stat), 0o644)
	}
	return base, path
}

func TestReadMemUsage(t *testing.T) {
	t.Run("subtracts inactive_file", func(t *testing.T) {
		base, path := writeCgroup(t, "1000000\n", "anon 500\ninactive_file 400000\nactive_file 10\n")
		got, ok := readMemUsage(base, path)
		if !ok || got != 600000 {
			t.Errorf("got (%d, %v), want (600000, true)", got, ok)
		}
	})
	t.Run("inactive_file larger than current is ignored", func(t *testing.T) {
		base, path := writeCgroup(t, "100\n", "inactive_file 400\n")
		got, ok := readMemUsage(base, path)
		if !ok || got != 100 {
			t.Errorf("got (%d, %v), want (100, true)", got, ok)
		}
	})
	t.Run("no memory.stat", func(t *testing.T) {
		base, path := writeCgroup(t, "4096\n", "")
		got, ok := readMemUsage(base, path)
		if !ok || got != 4096 {
			t.Errorf("got (%d, %v), want (4096, true)", got, ok)
		}
	})
	t.Run("no memory.current", func(t *testing.T) {
		base, path := writeCgroup(t, "", "")
		if _, ok := readMemUsage(base, path); ok {
			t.Error("ok = true, want false")
		}
	})
}

func TestShortImage(t *testing.T) {
	tests := []struct{ in, want string }{
		{"alpine", "alpine"},
		{"alpine:latest", "alpine"},
		{"allinurl/goaccess:latest", "allinurl/goaccess"},
		{"postgres:17.10-bookworm", "postgres:17.10-bookworm"},
		{"ghcr.io/open-webui/open-webui:main", "ghcr.io/open-webui/open-webui:main"},
		{"localhost:5000/app", "localhost:5000/app"},
		{"localhost:5000/app:latest", "localhost:5000/app"},
		{"nginx@sha256:abcdef", "nginx"},
		{"sha256:0123456789abcdef0123", "0123456789ab"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := shortImage(tt.in); got != tt.want {
			t.Errorf("shortImage(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
