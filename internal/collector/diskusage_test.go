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
		Sizes: map[string]sizeInfo{
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
		Sizes:   map[string]sizeInfo{"/srv/x": {Bytes: 20, Known: false}},
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
	// Both are binds outside any project folder, so they are nested roots.
	u := aggregateDisk(diskInput{
		Containers: []DiskContainer{
			{ID: "app", Name: "app", Project: "p", LogKnown: true, Binds: []string{"/srv/p"}},
			{ID: "db", Name: "db", Project: "p", LogKnown: true, Binds: []string{"/srv/p/db"}},
		},
		Sizes: map[string]sizeInfo{"/srv/p": known(100), "/srv/p/db": known(30)}, // full sizes
	}, time.Now())

	if u.BindsTotal != 100 || len(u.Binds) != 2 {
		t.Errorf("every byte once: total %d, binds %+v", u.BindsTotal, u.Binds)
	}
	if got := u.ByContainer["app"].Binds; got != 70 {
		t.Errorf("app keeps the outer directory minus the inner one: %d, want 70", got)
	}
	if got := u.ByContainer["db"].Binds; got != 30 {
		t.Errorf("db owns the inner directory: %d, want 30", got)
	}
}

func TestPlanRoots(t *testing.T) {
	plan := planRoots([]DiskContainer{
		// the project folder is also bound as "." (".:/app"), and so is a folder inside
		{ID: "web", Project: "a", WorkDir: "/srv/a", Binds: []string{"/srv/a", "/srv/a/data", "/srv/outside"}},
		{ID: "db", Project: "a", WorkDir: "/srv/a", Binds: []string{"/srv/outside"}},
		// the same folder used by a second project name
		{ID: "x", Project: "a2", WorkDir: "/srv/a"},
		// a container with a folder but no project, and a standalone one: no folder
		{ID: "lone", Binds: []string{"/srv/lone"}, WorkDir: "/srv/ignored"},
	})

	if want := []string{"a", "a2"}; !reflect.DeepEqual(plan.Folders["/srv/a"], want) {
		t.Errorf("owners of /srv/a = %v, want %v", plan.Folders["/srv/a"], want)
	}
	if _, ok := plan.Folders["/srv/ignored"]; ok || len(plan.Folders) != 1 {
		t.Errorf("only project folders count: %v", plan.Folders)
	}
	if _, ok := plan.Binds["/srv/a"]; ok {
		t.Error("a bind equal to the project folder is part of the files, not a bind")
	}
	if _, ok := plan.Binds["/srv/a/data"]; ok {
		t.Error("a bind inside the project folder is part of the files, not a bind")
	}
	if want := []string{"db", "web"}; !reflect.DeepEqual(plan.Binds["/srv/outside"], want) {
		t.Errorf("owners of /srv/outside = %v, want %v", plan.Binds["/srv/outside"], want)
	}
	if want := []string{"/srv/a", "/srv/lone", "/srv/outside"}; !reflect.DeepEqual(plan.Roots, want) {
		t.Errorf("roots = %v, want %v", plan.Roots, want)
	}
}

func TestExclusiveSizes(t *testing.T) {
	// /a contains /a/b, which contains /a/b/c; /z is separate
	got := exclusiveSizes(map[string]sizeInfo{
		"/a": known(1000), "/a/b": known(300), "/a/b/c": known(100), "/z": known(5),
	})
	want := map[string]int64{"/a": 700, "/a/b": 200, "/a/b/c": 100, "/z": 5}
	for p, w := range want {
		if got[p].Bytes != w || !got[p].Known {
			t.Errorf("%s = %+v, want %d bytes, known", p, got[p], w)
		}
	}

	// an unknown nested root makes the outer one unknown
	got = exclusiveSizes(map[string]sizeInfo{"/a": known(1000), "/a/b": {Bytes: 10, Known: false}})
	if got["/a"].Known {
		t.Errorf("the outer root must be unknown when an inner one is: %+v", got["/a"])
	}

	// never negative (the inner walk can be larger than the outer one if files changed)
	got = exclusiveSizes(map[string]sizeInfo{"/a": known(10), "/a/b": known(50)})
	if got["/a"].Bytes != 0 {
		t.Errorf("exclusive size must not be negative: %+v", got["/a"])
	}

	// a name prefix is not nesting
	got = exclusiveSizes(map[string]sizeInfo{"/srv/p": known(10), "/srv/pp": known(20)})
	if got["/srv/p"].Bytes != 10 || got["/srv/pp"].Bytes != 20 {
		t.Errorf("/srv/pp is not inside /srv/p: %+v", got)
	}
}

// TestProjectFolders is the case with a folder that contains a bind mount and a
// nested project: every byte is counted once.
func TestProjectFolders(t *testing.T) {
	u := aggregateDisk(diskInput{
		Containers: []DiskContainer{
			// project A: folder /srv/a, its container binds a directory inside the folder
			{ID: "a1", Name: "a-web-1", Project: "a", WorkDir: "/srv/a", LogKnown: true, Binds: []string{"/srv/a/data"}},
			// project B lives inside A's folder
			{ID: "b1", Name: "b-web-1", Project: "b", WorkDir: "/srv/a/b", LogKnown: true},
			// a bind outside every project folder
			{ID: "a2", Name: "a-db-1", Project: "a", WorkDir: "/srv/a", LogKnown: true, Binds: []string{"/srv/other"}},
		},
		Sizes: map[string]sizeInfo{
			"/srv/a":     known(1000), // includes /srv/a/data and /srv/a/b
			"/srv/a/b":   known(300),
			"/srv/other": known(50),
		},
	}, time.Now())

	a := findProject(t, u, "a")
	b := findProject(t, u, "b")
	if a.Files != 700 || b.Files != 300 {
		t.Errorf("files: a %d, b %d, want 700 and 300 (b is inside a, counted once)", a.Files, b.Files)
	}
	if a.Binds != 50 || b.Binds != 0 {
		t.Errorf("binds: a %d, b %d, want 50 and 0 (the bind inside the folder is part of the files)", a.Binds, b.Binds)
	}
	if u.FilesTotal != 1000 || u.BindsTotal != 50 {
		t.Errorf("totals: files %d binds %d, want 1000 and 50", u.FilesTotal, u.BindsTotal)
	}
	if u.DockerTotal != 1050 {
		t.Errorf("DockerTotal = %d, want 1050 (nothing counted twice)", u.DockerTotal)
	}
	if len(u.Folders) != 2 || len(u.Binds) != 1 || u.Binds[0].Path != "/srv/other" {
		t.Errorf("folders %+v binds %+v", u.Folders, u.Binds)
	}
	if bytes, unknown := u.FilesOf("a"); bytes != 700 || unknown {
		t.Errorf("FilesOf(a) = %d, %v", bytes, unknown)
	}
	if bytes, _ := u.FilesOf("nope"); bytes != 0 {
		t.Errorf("FilesOf(nope) = %d", bytes)
	}
	// per-container figures do not include the folder (it belongs to the project)
	if got := u.ByContainer["a1"].Files; got != 0 {
		t.Errorf("a container has no files of its own: %d", got)
	}
	if a.Total() != 700+50 || a.Complete() == false {
		t.Errorf("project total must include files: %+v", a)
	}
}

func TestProjectFolderSharedByTwoProjects(t *testing.T) {
	u := aggregateDisk(diskInput{
		Containers: []DiskContainer{
			{ID: "1", Name: "one", Project: "p1", WorkDir: "/srv/x", LogKnown: true},
			{ID: "2", Name: "two", Project: "p2", WorkDir: "/srv/x", LogKnown: true},
		},
		Sizes: map[string]sizeInfo{"/srv/x": known(100)},
	}, time.Now())
	if findProject(t, u, "p1").Files != 50 || findProject(t, u, "p2").Files != 50 || u.FilesTotal != 100 {
		t.Errorf("shared folder is split evenly and counted once: %+v", u.Projects)
	}
}

func TestSizeCache(t *testing.T) {
	c := newSizeCache(30 * time.Minute)
	t0 := time.Unix(1000, 0)
	var walked []string
	walk := func(sizes map[string]sizeInfo) func(string) sizeInfo {
		return func(root string) sizeInfo {
			walked = append(walked, root)
			return sizes[root]
		}
	}

	// first time: everything is walked
	got, oldest := c.sizes([]string{"/a", "/b"}, t0, walk(map[string]sizeInfo{"/a": known(10), "/b": known(20)}))
	if !reflect.DeepEqual(walked, []string{"/a", "/b"}) || got["/a"].Bytes != 10 || !oldest.Equal(t0) {
		t.Fatalf("first run: walked %v got %v oldest %v", walked, got, oldest)
	}

	// 5 minutes later: reused, nothing is walked, new sizes are ignored
	walked = nil
	got, oldest = c.sizes([]string{"/a", "/b"}, t0.Add(5*time.Minute), walk(map[string]sizeInfo{"/a": known(99), "/b": known(99)}))
	if len(walked) != 0 || got["/a"].Bytes != 10 || !oldest.Equal(t0) {
		t.Errorf("within 30 minutes: walked %v got %v oldest %v", walked, got, oldest)
	}

	// a new root is walked at once, the old ones are still reused
	walked = nil
	got, _ = c.sizes([]string{"/a", "/c"}, t0.Add(6*time.Minute), walk(map[string]sizeInfo{"/c": known(7)}))
	if !reflect.DeepEqual(walked, []string{"/c"}) || got["/c"].Bytes != 7 {
		t.Errorf("new root: walked %v got %v", walked, got)
	}
	if _, ok := c.entries["/b"]; ok {
		t.Error("a root that is gone must be forgotten")
	}

	// after 30 minutes /a is walked again
	walked = nil
	got, _ = c.sizes([]string{"/a"}, t0.Add(31*time.Minute), walk(map[string]sizeInfo{"/a": known(11)}))
	if !reflect.DeepEqual(walked, []string{"/a"}) || got["/a"].Bytes != 11 {
		t.Errorf("after 30 minutes: walked %v got %v", walked, got)
	}

	// a partial walk is not cached as fresh, and the older complete value wins
	walked = nil
	later := t0.Add(62 * time.Minute)
	partial := map[string]sizeInfo{"/a": {Bytes: 3, Known: false}}
	got, oldest = c.sizes([]string{"/a"}, later, walk(partial))
	if got["/a"].Bytes != 11 || !got["/a"].Known || !oldest.Equal(t0.Add(31*time.Minute)) {
		t.Errorf("stale but complete must win over partial: %+v oldest %v", got["/a"], oldest)
	}
	walked = nil
	c.sizes([]string{"/a"}, later.Add(5*time.Minute), walk(partial))
	if len(walked) != 1 {
		t.Errorf("the partial result must be retried at the next refresh, walked %v", walked)
	}

	// no older value: the partial one is shown, marked unknown, and retried
	c2 := newSizeCache(30 * time.Minute)
	got, _ = c2.sizes([]string{"/x"}, t0, walk(map[string]sizeInfo{"/x": {Bytes: 4, Known: false}}))
	if got["/x"].Known || got["/x"].Bytes != 4 {
		t.Errorf("partial without history: %+v", got["/x"])
	}
	if _, ok := c2.entries["/x"]; ok {
		t.Error("a partial result must not be cached")
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
