package report

import (
	"reflect"
	"testing"
	"time"
)

func baseState() *State {
	return &State{
		Version:  stateVersion,
		Time:     time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC),
		BootTime: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		Mounts: map[string]MountState{
			"/":     {Used: 40 << 30, Total: 100 << 30},
			"/data": {Used: 5 << 30, Total: 50 << 30},
		},
		DiskKnown: true,
		Projects: map[string]int64{
			projectKey("shop"):       10 << 30,
			projectKey("blog"):       2 << 30,
			containerKey("old-kuma"): 1 << 30,
		},
		Reclaimable: 3 << 30,
		Containers: map[string][]string{
			"shop":          {"web", "db"},
			"blog":          {"web"},
			standaloneGroup: {"kuma"},
		},
	}
}

func TestDiffWithoutPreviousReport(t *testing.T) {
	c := Diff(nil, baseState())
	if c.HasPrev {
		t.Error("nil previous state: HasPrev must be false")
	}
	if len(c.MountUsed) != 0 || len(c.NewMounts) != 0 || c.Rebooted {
		t.Errorf("nothing to compare, got %+v", c)
	}
}

func TestDiffNoChange(t *testing.T) {
	c := Diff(baseState(), baseState())
	if !c.HasPrev || !c.DiskCompared {
		t.Fatalf("both states are known: %+v", c)
	}
	for path, d := range c.MountUsed {
		if d != 0 {
			t.Errorf("mount %s changed by %d", path, d)
		}
	}
	if len(c.NewMounts)+len(c.GoneMounts)+len(c.NewProjects)+len(c.AddedGroups)+len(c.RemovedGroups)+len(c.AddedContainers)+len(c.RemovedContainers) != 0 {
		t.Errorf("no additions or removals expected: %+v", c)
	}
	if c.Rebooted || c.ReclaimDelta != 0 {
		t.Errorf("rebooted %v, reclaim %d", c.Rebooted, c.ReclaimDelta)
	}
}

func TestDiffChanges(t *testing.T) {
	prev := baseState()
	cur := baseState()
	cur.Time = time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC)

	cur.Mounts["/"] = MountState{Used: 42 << 30, Total: 100 << 30}   // grew by 2G
	cur.Mounts["/data"] = MountState{Used: 4 << 30, Total: 50 << 30} // shrank by 1G
	cur.Mounts["/backup"] = MountState{Used: 1 << 30, Total: 10 << 30}
	delete(cur.Mounts, "/boot") // not there before either

	cur.Projects[projectKey("shop")] = 12 << 30    // +2G
	cur.Projects[projectKey("blog")] = 1 << 30     // -1G
	delete(cur.Projects, containerKey("old-kuma")) // gone: no delta, not a new one
	cur.Projects[projectKey("wiki")] = 500 << 20   // new
	cur.Reclaimable = 5 << 30                      // +2G

	cur.Containers["shop"] = []string{"web", "cache"} // db removed, cache added
	cur.Containers["wiki"] = []string{"app"}          // new project
	delete(cur.Containers, "blog")                    // removed project

	c := Diff(prev, cur)

	if c.MountUsed["/"] != 2<<30 || c.MountUsed["/data"] != -(1<<30) {
		t.Errorf("mount changes: %+v", c.MountUsed)
	}
	if !reflect.DeepEqual(c.NewMounts, []string{"/backup"}) || len(c.GoneMounts) != 0 {
		t.Errorf("mounts: new %v gone %v", c.NewMounts, c.GoneMounts)
	}
	if c.ProjectDelta[projectKey("shop")] != 2<<30 || c.ProjectDelta[projectKey("blog")] != -(1<<30) {
		t.Errorf("project changes: %+v", c.ProjectDelta)
	}
	if !reflect.DeepEqual(c.NewProjects, []string{projectKey("wiki")}) {
		t.Errorf("new projects: %v", c.NewProjects)
	}
	if _, ok := c.ProjectDelta[containerKey("old-kuma")]; ok {
		t.Error("a project that is gone has no delta")
	}
	if c.ReclaimDelta != 2<<30 {
		t.Errorf("reclaim delta %d", c.ReclaimDelta)
	}
	if !reflect.DeepEqual(c.AddedGroups, []string{"wiki"}) || !reflect.DeepEqual(c.RemovedGroups, []string{"blog"}) {
		t.Errorf("groups: added %v removed %v", c.AddedGroups, c.RemovedGroups)
	}
	if !reflect.DeepEqual(c.AddedContainers, []string{"shop/cache"}) || !reflect.DeepEqual(c.RemovedContainers, []string{"shop/db"}) {
		t.Errorf("containers: added %v removed %v", c.AddedContainers, c.RemovedContainers)
	}
}

func TestDiffGoneMount(t *testing.T) {
	cur := baseState()
	delete(cur.Mounts, "/data")
	if c := Diff(baseState(), cur); !reflect.DeepEqual(c.GoneMounts, []string{"/data"}) {
		t.Errorf("gone mounts: %v", c.GoneMounts)
	}
}

func TestDiffReboot(t *testing.T) {
	prev := baseState() // booted 1 Oct, previous report 24 Oct
	cur := baseState()
	cur.BootTime = prev.BootTime
	if Diff(prev, cur).Rebooted {
		t.Error("same boot time: no reboot")
	}
	cur.BootTime = time.Date(2026, 10, 24, 22, 0, 0, 0, time.UTC) // booted after the previous report
	if !Diff(prev, cur).Rebooted {
		t.Error("booted after the previous report: reboot")
	}
}

func TestDiffWithoutDiskFigures(t *testing.T) {
	prev := baseState()
	prev.DiskKnown = false // Docker was not reachable last time
	c := Diff(prev, baseState())
	if c.DiskCompared || len(c.ProjectDelta) != 0 || len(c.NewProjects) != 0 {
		t.Errorf("disk figures can only be compared when both are known: %+v", c)
	}
	if !c.HasPrev || len(c.MountUsed) == 0 {
		t.Error("mounts and containers are still compared")
	}
}
