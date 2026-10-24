package collector

import "testing"

const id = "3f2a1c9b4d7e0a8b6c5d4e3f2a1b0c9d8e7f6a5b4c3d2e1f0a9b8c7d6e5f4a3b"

func TestParseCgroup(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantKind Kind
		wantID   string
		wantUnit string
		wantPath string
	}{
		{
			name:     "docker with systemd driver (cgroup v2)",
			content:  "0::/system.slice/docker-" + id + ".scope\n",
			wantKind: KindContainer,
			wantID:   id,
			wantPath: "/system.slice/docker-" + id + ".scope",
		},
		{
			name:     "docker with systemd driver, nested cgroup",
			content:  "0::/system.slice/docker-" + id + ".scope/init.scope\n",
			wantKind: KindContainer,
			wantID:   id,
			wantPath: "/system.slice/docker-" + id + ".scope",
		},
		{
			name:     "docker with cgroupfs driver (cgroup v1)",
			content:  "12:memory:/docker/" + id + "\n1:name=systemd:/docker/" + id + "\n",
			wantKind: KindContainer,
			wantID:   id,
			wantPath: "/docker/" + id,
		},
		{
			name:     "systemd service",
			content:  "0::/system.slice/ssh.service\n",
			wantKind: KindService,
			wantUnit: "ssh.service",
		},
		{
			name:     "innermost service wins",
			content:  "0::/user.slice/user-1000.slice/user@1000.service/app.slice/foo.service\n",
			wantKind: KindService,
			wantUnit: "foo.service",
		},
		{
			name:     "user session scope is host",
			content:  "0::/user.slice/user-1000.slice/session-3.scope\n",
			wantKind: KindHost,
		},
		{
			name:     "hybrid: v1 line ignored, unified line used",
			content:  "1:net_cls:/\n0::/user.slice/user-1000.slice/user@1000.service/app.slice/app-code.scope\n",
			wantKind: KindService,
			wantUnit: "user@1000.service",
		},
		{name: "empty", content: "", wantKind: KindHost},
		{name: "root cgroup", content: "0::/\n", wantKind: KindHost},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseCgroup(tt.content)
			if got.Kind != tt.wantKind {
				t.Fatalf("Kind = %v, want %v", got.Kind, tt.wantKind)
			}
			if got.ContainerID != tt.wantID {
				t.Errorf("ContainerID = %q, want %q", got.ContainerID, tt.wantID)
			}
			if got.Unit != tt.wantUnit {
				t.Errorf("Unit = %q, want %q", got.Unit, tt.wantUnit)
			}
			if got.CgroupPath != tt.wantPath {
				t.Errorf("CgroupPath = %q, want %q", got.CgroupPath, tt.wantPath)
			}
		})
	}
}

func TestOriginString(t *testing.T) {
	tests := []struct {
		o    Origin
		want string
	}{
		{Origin{Kind: KindHost}, "host"},
		{Origin{Kind: KindService, Unit: "ssh.service"}, "systemd: ssh.service"},
		{Origin{Kind: KindContainer, ContainerID: id}, "container 3f2a1c9b4d7e"},
		{Origin{Kind: KindContainer, ContainerID: id, ContainerName: "uptime-kuma"}, "container uptime-kuma"},
		{Origin{Kind: KindContainer, ContainerID: id, ContainerName: "lienks-app-clickhouse-1", Project: "lienks-app", Service: "clickhouse"}, "container lienks-app/clickhouse"},
	}
	for _, tt := range tests {
		if got := tt.o.String(); got != tt.want {
			t.Errorf("String() = %q, want %q", got, tt.want)
		}
	}
}
