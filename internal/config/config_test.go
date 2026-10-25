package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustParse(t *testing.T, yamlText string) *Config {
	t.Helper()
	cfg, err := Parse(strings.NewReader(yamlText))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cfg
}

// The example file is documentation, so it must always load.
func TestExampleFileParses(t *testing.T) {
	data, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg := mustParse(t, string(data))

	if cfg.Server.Name != "main-vps" {
		t.Errorf("server.name = %q", cfg.Server.Name)
	}
	if cfg.Alerts.CPU.Process.Percent != 80 || cfg.Alerts.CPU.Process.Duration != 15*time.Minute {
		t.Errorf("cpu.process = %+v", cfg.Alerts.CPU.Process)
	}
	if cfg.Alerts.CPU.Total.Percent != 90 || cfg.Alerts.CPU.Total.Duration != 10*time.Minute {
		t.Errorf("cpu.total = %+v", cfg.Alerts.CPU.Total)
	}
	if cfg.Alerts.Disk.Percent != 90 {
		t.Errorf("disk = %+v", cfg.Alerts.Disk)
	}
	s := cfg.SMTP
	if s.Host != "smtp.example.com" || s.Port != 587 || s.PasswordEnv != "ETERNALSHELL_SMTP_PASSWORD" || len(s.To) != 1 {
		t.Errorf("smtp = %+v", s)
	}
	if err := cfg.ValidateSMTP(); err != nil {
		t.Errorf("example smtp section should be valid: %v", err)
	}
}

func TestDefaultsApplyToMissingKeys(t *testing.T) {
	cfg := mustParse(t, "smtp:\n  host: mail.example.com\n")
	def := Default()

	if cfg.Alerts.CPU.Process != def.Alerts.CPU.Process || cfg.Alerts.Disk.Percent != 90 {
		t.Errorf("alert defaults lost: %+v", cfg.Alerts)
	}
	if cfg.SMTP.Port != 587 {
		t.Errorf("smtp.port default = %d, want 587", cfg.SMTP.Port)
	}
	host, _ := os.Hostname()
	if cfg.Server.Name != host {
		t.Errorf("server.name default = %q, want the host name %q", cfg.Server.Name, host)
	}
}

func TestEmptyFileGivesDefaults(t *testing.T) {
	cfg := mustParse(t, "")
	if cfg.Alerts.CPU.Total.Percent != 90 {
		t.Errorf("empty file should give defaults, got %+v", cfg.Alerts)
	}
}

func TestPartialOverrideKeepsOtherDefaults(t *testing.T) {
	cfg := mustParse(t, "alerts:\n  cpu:\n    process: { percent: 50 }\n")
	if cfg.Alerts.CPU.Process.Percent != 50 {
		t.Errorf("percent = %v", cfg.Alerts.CPU.Process.Percent)
	}
	if cfg.Alerts.CPU.Process.Duration != 15*time.Minute {
		t.Errorf("duration should stay at its default, got %v", cfg.Alerts.CPU.Process.Duration)
	}
}

func TestUnknownKeyIsRejected(t *testing.T) {
	_, err := Parse(strings.NewReader("smtp:\n  host: a\n  pasword_env: X\n"))
	if err == nil {
		t.Fatal("a typo in a key must be an error")
	}
	if !strings.Contains(err.Error(), "pasword_env") || !strings.Contains(err.Error(), "line 3") {
		t.Errorf("error should name the key and the line: %v", err)
	}
}

func TestPasswordKeyIsRejected(t *testing.T) {
	for _, key := range []string{"password", "Password"} {
		_, err := Parse(strings.NewReader("smtp:\n  host: a\n  " + key + ": hunter2\n"))
		if err == nil {
			t.Fatalf("%q in the file must be rejected", key)
		}
		if !strings.Contains(err.Error(), "password_env") {
			t.Errorf("error should point to password_env: %v", err)
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("error must not repeat the password: %v", err)
		}
	}
}

func TestSyntaxError(t *testing.T) {
	if _, err := Parse(strings.NewReader("smtp: [unclosed\n")); err == nil {
		t.Error("broken YAML must be an error")
	}
}

func TestValidateReportsAllProblems(t *testing.T) {
	_, err := Parse(strings.NewReader(`
alerts:
  cpu:
    process: { percent: 150, duration: 0s }
    total:   { percent: 0 }
  disk: { percent: -5 }
`))
	if err == nil {
		t.Fatal("expected validation errors")
	}
	msg := err.Error()
	for _, want := range []string{
		"alerts.cpu.process.percent", "alerts.cpu.process.duration",
		"alerts.cpu.total.percent", "alerts.disk.percent",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should mention %q:\n%s", want, msg)
		}
	}
}

func TestRemovedSSHBlockHasAClearMessage(t *testing.T) {
	_, err := Parse(strings.NewReader("alerts:\n  ssh:\n    allowed_users: [philip]\n"))
	if err == nil || !strings.Contains(err.Error(), "alerts.ssh was removed") {
		t.Errorf("want a clear message about the removed ssh block, got %v", err)
	}
}

func TestValidateSMTP(t *testing.T) {
	ok := func() *Config {
		c := Default()
		c.SMTP = SMTP{Host: "smtp.example.com", Port: 587, Username: "a@example.com", PasswordEnv: "PW", To: []string{"b@example.com"}}
		return &c
	}
	if err := ok().ValidateSMTP(); err != nil {
		t.Fatalf("valid smtp section rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"no host", func(c *Config) { c.SMTP.Host = "" }, "smtp.host"},
		{"port 0", func(c *Config) { c.SMTP.Port = 0 }, "smtp.port"},
		{"port too big", func(c *Config) { c.SMTP.Port = 70000 }, "smtp.port"},
		{"no username", func(c *Config) { c.SMTP.Username = "" }, "smtp.username"},
		{"no password_env", func(c *Config) { c.SMTP.PasswordEnv = "" }, "smtp.password_env"},
		{"no recipient", func(c *Config) { c.SMTP.To = nil }, "smtp.to"},
		{"bad recipient", func(c *Config) { c.SMTP.To = []string{"nope"} }, `"nope"`},
		{"bad from", func(c *Config) { c.SMTP.From = "x y z" }, "smtp.from"},
	}
	for _, tt := range tests {
		c := ok()
		tt.mutate(c)
		err := c.ValidateSMTP()
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: error = %v, want it to mention %q", tt.name, err, tt.want)
		}
	}

	// the TUI-side validation must not demand any SMTP settings
	empty := Default()
	if err := empty.Validate(); err != nil {
		t.Errorf("Validate() should not require smtp: %v", err)
	}
}

func TestFromAddress(t *testing.T) {
	s := SMTP{Username: "user@example.com"}
	if s.FromAddress() != "user@example.com" {
		t.Errorf("from should default to the username, got %q", s.FromAddress())
	}
	s.From = "alerts@example.com"
	if s.FromAddress() != "alerts@example.com" {
		t.Errorf("explicit from ignored, got %q", s.FromAddress())
	}
}

func TestPassword(t *testing.T) {
	s := SMTP{PasswordEnv: "ES_TEST_SMTP_PW"}

	t.Setenv("ES_TEST_SMTP_PW", "")
	_, err := s.Password()
	if err == nil || !strings.Contains(err.Error(), "ES_TEST_SMTP_PW") {
		t.Errorf("unset variable: error should name the variable, got %v", err)
	}

	t.Setenv("ES_TEST_SMTP_PW", "s3cret-value")
	got, err := s.Password()
	if err != nil || got != "s3cret-value" {
		t.Errorf("got (%q, %v)", got, err)
	}

	if _, err := (SMTP{}).Password(); err == nil {
		t.Error("missing password_env must be an error")
	}
}

func TestPasswordNeverInErrors(t *testing.T) {
	t.Setenv("ES_TEST_SMTP_PW", "s3cret-value")
	c := Default()
	c.SMTP = SMTP{PasswordEnv: "ES_TEST_SMTP_PW"} // everything else missing
	err := c.ValidateSMTP()
	if err == nil {
		t.Fatal("expected errors")
	}
	if strings.Contains(err.Error(), "s3cret-value") {
		t.Errorf("password leaked into an error: %v", err)
	}
}

func TestFind(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	// nothing there: the error lists both default paths
	_, err := Find("")
	if err == nil || !strings.Contains(err.Error(), SystemPath) || !strings.Contains(err.Error(), LocalPath) {
		// /etc/eternalshell/config.yaml may exist on a real server; then Find succeeds
		if err != nil {
			t.Errorf("error should list both paths: %v", err)
		}
	}

	// ./config.yaml is found
	if err := os.WriteFile(LocalPath, []byte("server: {name: x}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(SystemPath); statErr != nil {
		got, err := Find("")
		if err != nil || got != LocalPath {
			t.Errorf("Find(\"\") = (%q, %v), want %q", got, err, LocalPath)
		}
	}

	// an explicit path wins
	other := filepath.Join(dir, "other.yaml")
	os.WriteFile(other, []byte(""), 0o600)
	if got, err := Find(other); err != nil || got != other {
		t.Errorf("Find(explicit) = (%q, %v)", got, err)
	}

	// an explicit path that does not exist is an error, with no fallback to ./config.yaml
	_, err = Find(filepath.Join(dir, "missing.yaml"))
	if err == nil || !strings.Contains(err.Error(), "missing.yaml") {
		t.Errorf("missing explicit file: error = %v", err)
	}
}

func TestPermissionWarning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		mode os.FileMode
		warn bool
	}{
		{0o600, false},
		{0o400, false},
		{0o640, false}, // group read is fine (root:eternalshell 0640 for the service)
		{0o660, true},  // group can write
		{0o644, true},
		{0o606, true}, // others can read
		{0o666, true},
	}
	for _, tt := range tests {
		if err := os.Chmod(path, tt.mode); err != nil {
			t.Fatal(err)
		}
		got := PermissionWarning(path)
		if (got != "") != tt.warn {
			t.Errorf("mode %04o: warning = %q, want warning: %v", tt.mode, got, tt.warn)
		}
		if tt.warn && (!strings.Contains(got, "chmod 640") || !strings.Contains(got, path)) {
			t.Errorf("mode %04o: warning should give the fix: %q", tt.mode, got)
		}
	}

	if PermissionWarning(filepath.Join(t.TempDir(), "nope")) != "" {
		t.Error("a missing file has nothing to warn about (Load reports it)")
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("server: {name: test-box}\nalerts: {disk: {percent: 95}}\n"), 0o600)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Name != "test-box" || cfg.Alerts.Disk.Percent != 95 {
		t.Errorf("cfg = %+v", cfg)
	}

	// errors name the file
	bad := filepath.Join(dir, "bad.yaml")
	os.WriteFile(bad, []byte("alerts: {disk: {percent: 500}}\n"), 0o600)
	if _, err := Load(bad); err == nil || !strings.Contains(err.Error(), "bad.yaml") {
		t.Errorf("error should name the file: %v", err)
	}

	if _, err := Load(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Error("missing file must be an error")
	}
}

func TestReportDefaultsAndParsing(t *testing.T) {
	// absent: off, but with usable defaults
	cfg := mustParse(t, "")
	r := cfg.Alerts.Report
	if r.Enabled || r.Schedule != "daily" || r.Weekday != "monday" || r.Time != "08:00" {
		t.Errorf("defaults = %+v", r)
	}

	cfg = mustParse(t, "alerts:\n  report:\n    enabled: true\n    schedule: weekly\n    weekday: Fri\n    time: \"06:30\"\n")
	r = cfg.Alerts.Report
	if !r.Enabled || r.Schedule != "weekly" || r.Weekday != "Fri" || r.Time != "06:30" {
		t.Errorf("parsed = %+v", r)
	}
}

func TestReportValidation(t *testing.T) {
	bad := map[string]string{
		"alerts.report.schedule": "alerts:\n  report: {schedule: monthly}\n",
		"alerts.report.weekday":  "alerts:\n  report: {schedule: weekly, weekday: someday}\n",
		"alerts.report.time":     "alerts:\n  report: {time: \"8am\"}\n",
	}
	for want, y := range bad {
		if _, err := Parse(strings.NewReader(y)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error = %v", want, err)
		}
	}
	for _, tm := range []string{"24:00", "08:60", "8:00", "08:0", "0800", ""} {
		if _, _, err := ParseClock(tm); err == nil {
			t.Errorf("ParseClock(%q) should fail", tm)
		}
	}
	if h, m, err := ParseClock("23:59"); err != nil || h != 23 || m != 59 {
		t.Errorf("ParseClock(23:59) = %d:%d, %v", h, m, err)
	}
	for in, want := range map[string]time.Weekday{"monday": time.Monday, "Mon": time.Monday, "SUNDAY": time.Sunday, " sat ": time.Saturday} {
		if got, err := ParseWeekday(in); err != nil || got != want {
			t.Errorf("ParseWeekday(%q) = %v, %v", in, got, err)
		}
	}
}
