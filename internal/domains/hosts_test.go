package domains

import (
	"reflect"
	"testing"
)

func TestHostsFromRule(t *testing.T) {
	tests := []struct {
		name string
		rule string
		want []string
	}{
		{"v3 single host", "Host(`app.example.com`)", []string{"app.example.com"}},
		{"v2 several hosts in one call", "Host(`a.example.com`, `b.example.com`)", []string{"a.example.com", "b.example.com"}},
		{"or combination", "Host(`a.example.com`) || Host(`b.example.com`)", []string{"a.example.com", "b.example.com"}},
		{"and with a path prefix", "Host(`a.example.com`) && PathPrefix(`/api`)", []string{"a.example.com"}},
		{"parentheses around the whole rule", "(Host(`a.example.com`) || Host(`b.example.com`)) && PathPrefix(`/x`)", []string{"a.example.com", "b.example.com"}},
		{"same host twice", "Host(`a.example.com`) || Host(`a.example.com`)", []string{"a.example.com"}},
		{"HostRegexp is ignored", "HostRegexp(`{sub:[a-z]+}.example.com`)", nil},
		{"HostSNI is ignored", "HostSNI(`db.example.com`)", nil},
		{"HostRegexp next to Host", "Host(`a.example.com`) || HostRegexp(`.+\\.example\\.org`)", []string{"a.example.com"}},
		{"uppercase and a trailing dot", "Host(`App.Example.COM.`)", []string{"app.example.com"}},
		{"no host at all", "PathPrefix(`/x`)", nil},
		{"empty", "", nil},
	}
	for _, tt := range tests {
		if got := HostsFromRule(tt.rule); !reflect.DeepEqual(got, tt.want) && !(len(got) == 0 && len(tt.want) == 0) {
			t.Errorf("%s: HostsFromRule(%q) = %v, want %v", tt.name, tt.rule, got, tt.want)
		}
	}
}

func TestHostsRejectUnsafeNames(t *testing.T) {
	bad := []string{
		"Host(`127.0.0.1`)", "Host(`169.254.169.254`)", "Host(`::1`)", "Host(`[::1]`)", "Host(`10.0.0.5`)",
		"Host(`localhost`)", "Host(`app.localhost`)",
		"Host(`*.example.com`)", "Host(`{host}.example.com`)", "Host(`a b.example.com`)",
		"Host(`a.example.com/path`)", "Host(`a.example.com:8080`)", "Host(`-bad.example.com`)",
		"Host(`evil\x1b[2J.example.com`)", "Host(`a\n.example.com`)",
	}
	for _, rule := range bad {
		if got := HostsFromRule(rule); len(got) != 0 {
			t.Errorf("HostsFromRule(%q) = %v, want nothing", rule, got)
		}
	}
	// a good host next to a bad one still comes through
	if got := HostsFromRule("Host(`127.0.0.1`) || Host(`ok.example.com`)"); !reflect.DeepEqual(got, []string{"ok.example.com"}) {
		t.Errorf("got %v", got)
	}
}

func TestHostsFromLabels(t *testing.T) {
	labels := map[string]string{
		"traefik.http.routers.app-http.rule":    "Host(`app.example.com`)",
		"traefik.http.routers.app-https.rule":   "Host(`app.example.com`)", // shares the host with the http router
		"traefik.http.routers.api.rule":         "Host(`api.example.com`) || Host(`api2.example.com`)",
		"traefik.tcp.routers.db.rule":           "HostSNI(`db.example.com`)",
		"traefik.http.routers.app-https.tls":    "true",
		"traefik.http.services.app.rule.note":   "Host(`not-a-rule-key.example.com`)", // key does not end in .rule
		"com.docker.compose.project":            "app",
		"traefik.http.routers.skipme.rulething": "Host(`nope.example.com`)",
	}
	want := []string{"api.example.com", "api2.example.com", "app.example.com"}
	if got := HostsFromLabels(labels); !reflect.DeepEqual(got, want) {
		t.Errorf("HostsFromLabels = %v, want %v", got, want)
	}
	if got := HostsFromLabels(nil); len(got) != 0 {
		t.Errorf("nil labels: %v", got)
	}
}
