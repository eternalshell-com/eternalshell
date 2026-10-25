package collector

import (
	"reflect"
	"testing"
	"time"
)

func known(n int64) sizeInfo { return sizeInfo{Bytes: n, Known: true} }

func findProject(t *testing.T, u *DiskUsage, name string) ProjectDisk {
	t.Helper()
	for _, p := range u.Projects {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("project %q not found in %+v", name, u.Projects)
	return ProjectDisk{}
}

func TestAggregateDisk(t *testing.T) {
	in := diskInput{
		RootDir: "/var/lib/docker",
		Containers: []DiskContainer{
			// compose project "shop": web and db
			{ID: "web", Name: "shop-web-1", Project: "shop", SizeRw: 10, LogBytes: 5, LogKnown: true,
				Volumes: []string{"shared"}, Binds: []string{"/home/u/shop/data"}},
			{ID: "db", Name: "shop-db-1", Project: "shop", SizeRw: 20, LogBytes: 5, LogKnown: true,
				Volumes: []string{"shared", "pgdata"}, Binds: []string{"/home/u/shop/data"}},
			// a standalone container with its own bind and an unknown log size
			{ID: "kuma", Name: "uptime-kuma", SizeRw: 7, LogKnown: false, Binds: []string{"/srv/kuma"}},
		},
		Volumes: map[string]sizeInfo{
			"shared": known(100), // used by web and db: 50 each
			"pgdata": known(400), // used by db only
			"unused": known(30),  // used by nobody
		},
		Binds: map[string]sizeInfo{
			"/home/u/shop/data": known(200), // shared by web and db: 100 each
			"/srv/kuma":         known(60),
		},
		Images: 1000, Dangling: 40, BuildCache: 500,
	}

	u := aggregateDisk(in, time.Unix(0, 0))

	web := u.ByContainer["web"]
	if web.Layers != 10 || web.Volumes != 50 || web.Binds != 100 || web.Logs != 5 || web.Total() != 165 {
		t.Errorf("web = %+v", web)
	}
	db := u.ByContainer["db"]
	if db.Layers != 20 || db.Volumes != 450 || db.Binds != 100 || db.Logs != 5 || db.Total() != 575 {
		t.Errorf("db = %+v (shared volume and bind are split evenly)", db)
	}

	shop := findProject(t, u, "shop")
	if shop.Total() != 740 || shop.Standalone || len(shop.Containers) != 2 {
		t.Errorf("shop = %+v, want total 740 (165+575)", shop)
	}
	if shop.Volumes != 500 || shop.Binds != 200 {
		t.Errorf("project totals must add up to the real volume/bind sizes, got volumes %d binds %d", shop.Volumes, shop.Binds)
	}

	kuma := findProject(t, u, "uptime-kuma")
	if !kuma.Standalone || kuma.Binds != 60 || kuma.Layers != 7 {
		t.Errorf("kuma = %+v", kuma)
	}
	if !kuma.LogsUnknown || kuma.Complete() {
		t.Errorf("unknown logs must show on the project: %+v", kuma)
	}
	if shop.LogsUnknown || !shop.Complete() {
		t.Errorf("shop is fully known: %+v", shop)
	}

	// Totals
	if u.BindsTotal != 260 || len(u.Binds) != 2 {
		t.Errorf("a shared bind directory counts once: total %d, binds %+v", u.BindsTotal, u.Binds)
	}
	if u.Volumes != 530 || u.UnusedVolumes != 30 {
		t.Errorf("volumes %d unused %d", u.Volumes, u.UnusedVolumes)
	}
	if u.Reclaimable != 70 { // unused volume 30 + dangling images 40
		t.Errorf("reclaimable = %d, want 70", u.Reclaimable)
	}
	// layers 37 + logs 10 + volumes 530 + binds 260 + images 1000 + cache 500
	if u.DockerTotal != 2337 {
		t.Errorf("DockerTotal = %d, want 2337", u.DockerTotal)
	}
	if !u.Unknown || !u.LogsUnknown {
		t.Error("unknown logs must mark the whole result")
	}

	// The unused volume belongs to no project.
	var sum int64
	for _, p := range u.Projects {
		sum += p.Volumes
	}
	if sum != 500 {
		t.Errorf("sum of project volumes = %d, want 500 (the unused 30 is in no project)", sum)
	}

	// Biggest project first.
	if u.Projects[0].Name != "shop" {
		t.Errorf("projects should be sorted by total, got %q first", u.Projects[0].Name)
	}
	if p := u.ProjectOf("db"); p == nil || p.Name != "shop" {
		t.Errorf("ProjectOf(db) = %+v", p)
	}
}

func TestAggregateStandaloneContainersAreSeparate(t *testing.T) {
	u := aggregateDisk(diskInput{
		Containers: []DiskContainer{
			{ID: "a", Name: "alpha", SizeRw: 1, LogKnown: true},
			{ID: "b", Name: "beta", SizeRw: 2, LogKnown: true},
		},
	}, time.Now())
	if len(u.Projects) != 2 || !u.Projects[0].Standalone || !u.Projects[1].Standalone {
		t.Errorf("each standalone container is its own entry, got %+v", u.Projects)
	}
}

func TestAggregateUnknownBindAndVolumePropagate(t *testing.T) {
	u := aggregateDisk(diskInput{
		Containers: []DiskContainer{{ID: "a", Name: "a", Project: "p", LogKnown: true,
			Volumes: []string{"v"}, Binds: []string{"/srv/x"}}},
		Volumes: map[string]sizeInfo{"v": {Bytes: 10, Known: false}},
		Binds:   map[string]sizeInfo{"/srv/x": {Bytes: 20, Known: false}},
	}, time.Now())

	p := findProject(t, u, "p")
	if !p.VolumesUnknown || !p.BindsUnknown || p.Complete() {
		t.Errorf("unknown parts must propagate: %+v", p)
	}
	if p.Volumes != 10 || p.Binds != 20 {
		t.Errorf("the measured bytes are kept: %+v", p)
	}
	if !u.Unknown {
		t.Error("the result must be marked unknown")
	}
}

func TestNestedBindPathIsNotCountedTwice(t *testing.T) {
	// "app" mounts the whole project directory, "db" only a folder inside it.
	u := aggregateDisk(diskInput{
		Containers: []DiskContainer{
			{ID: "app", Name: "app", Project: "p", LogKnown: true, Binds: []string{"/srv/p"}},
			{ID: "db", Name: "db", Project: "p", LogKnown: true, Binds: []string{"/srv/p/db"}},
		},
		Binds: map[string]sizeInfo{"/srv/p": known(100)},
	}, time.Now())

	if u.BindsTotal != 100 || len(u.Binds) != 1 || u.Binds[0].Path != "/srv/p" {
		t.Errorf("only the outer directory counts: total %d, binds %+v", u.BindsTotal, u.Binds)
	}
	if got := u.ByContainer["app"].Binds + u.ByContainer["db"].Binds; got != 100 {
		t.Errorf("the two containers share the outer directory: %d", got)
	}
}

func TestCollapseBinds(t *testing.T) {
	eff, unique := collapseBinds(map[string][]string{
		"a": {"/srv/p/db", "/srv/p/cache"},
		"b": {"/srv/p"},
		"c": {"/srv/other"},
	})
	if want := []string{"/srv/other", "/srv/p"}; !reflect.DeepEqual(unique, want) {
		t.Errorf("unique = %v, want %v", unique, want)
	}
	if want := []string{"/srv/p"}; !reflect.DeepEqual(eff["a"], want) {
		t.Errorf("eff[a] = %v, want %v (both inner paths collapse into one)", eff["a"], want)
	}
	// a path that only shares a name prefix is not nested
	_, unique = collapseBinds(map[string][]string{"a": {"/srv/p"}, "b": {"/srv/pp"}})
	if len(unique) != 2 {
		t.Errorf("/srv/pp is not inside /srv/p, got %v", unique)
	}
}

func TestSkipBindPath(t *testing.T) {
	skipped := []string{
		"/", "/etc", "/etc/ssl/certs", "/proc", "/proc/1", "/sys/fs/cgroup", "/dev", "/dev/shm",
		"/run", "/run/docker.sock", "/var/run", "/var/run/docker.sock",
		"/var/lib/docker", "/var/lib/docker/volumes/x",
	}
	for _, p := range skipped {
		if !skipBindPath(p) {
			t.Errorf("skipBindPath(%q) = false, want true", p)
		}
	}
	counted := []string{"/home/u/app/data", "/srv/x", "/var/lib/postgres", "/etcetera", "/var/lib/dockerdata", "/data"}
	for _, p := range counted {
		if skipBindPath(p) {
			t.Errorf("skipBindPath(%q) = true, want false", p)
		}
	}
}

func TestMountFor(t *testing.T) {
	mounts := []Mount{{Path: "/"}, {Path: "/var"}, {Path: "/var/lib/docker"}, {Path: "/home"}}
	tests := map[string]string{
		"/var/lib/docker":         "/var/lib/docker",
		"/var/lib/docker/volumes": "/var/lib/docker",
		"/var/log":                "/var",
		"/varnish":                "/", // not inside /var
		"/home/u/app":             "/home",
		"/srv/x":                  "/",
	}
	for p, want := range tests {
		m := MountFor(mounts, p)
		if m == nil || m.Path != want {
			t.Errorf("MountFor(%q) = %+v, want %q", p, m, want)
		}
	}
	if MountFor(nil, "/x") != nil {
		t.Error("no mounts, no match")
	}
}
