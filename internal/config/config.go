// Package config loads and validates the EternalShell config file.
//
// The TUI works without any config. Only the watcher and the mail test need it.
// The SMTP password is never read from the file: the file only names the
// environment variable that holds it.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"os"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const (
	// SystemPath is where the config lives on a server.
	SystemPath = "/etc/eternalshell/config.yaml"

	// LocalPath is the fallback for local development.
	LocalPath = "config.yaml"
)

// Config is the whole config file. Field names match KONZEPT.md.
type Config struct {
	Server Server `yaml:"server"`
	Alerts Alerts `yaml:"alerts"`
	SMTP   SMTP   `yaml:"smtp"`
}

// Server identifies this machine in mails.
type Server struct {
	Name string `yaml:"name"`
}

// Alerts holds the thresholds of the three alerts.
type Alerts struct {
	CPU    CPUAlerts `yaml:"cpu"`
	Disk   DiskAlert `yaml:"disk"`
	Report Report    `yaml:"report"`
}

// Report is the periodic status mail ("all OK / this changed").
type Report struct {
	Enabled  bool   `yaml:"enabled"`
	Schedule string `yaml:"schedule"` // daily or weekly
	Weekday  string `yaml:"weekday"`  // only used for weekly, e.g. monday
	Time     string `yaml:"time"`     // HH:MM, server local time
}

// CPUAlerts: one process above a threshold, or the whole server.
type CPUAlerts struct {
	Process Threshold `yaml:"process"`
	Total   Threshold `yaml:"total"`
}

// Threshold fires when Percent is exceeded for at least Duration.
type Threshold struct {
	Percent  float64       `yaml:"percent"`
	Duration time.Duration `yaml:"duration"`
}

// DiskAlert fires when a mount is fuller than Percent.
type DiskAlert struct {
	Percent float64 `yaml:"percent"`
}

// SMTP is the mail server. The password is not part of this struct on purpose.
type SMTP struct {
	Host        string   `yaml:"host"`
	Port        int      `yaml:"port"`
	Username    string   `yaml:"username"`
	PasswordEnv string   `yaml:"password_env"` // name of the environment variable
	From        string   `yaml:"from"`         // optional, defaults to Username
	To          []string `yaml:"to"`
}

// Default returns the config with all default values filled in.
func Default() Config {
	host, _ := os.Hostname()
	return Config{
		Server: Server{Name: host},
		Alerts: Alerts{
			CPU: CPUAlerts{
				Process: Threshold{Percent: 80, Duration: 15 * time.Minute},
				Total:   Threshold{Percent: 90, Duration: 10 * time.Minute},
			},
			Disk:   DiskAlert{Percent: 90},
			Report: Report{Enabled: false, Schedule: "daily", Weekday: "monday", Time: "08:00"},
		},
		SMTP: SMTP{Port: 587},
	}
}

// Find decides which config file to use. An explicit path must exist: a typo
// must not silently fall back to another file. Without one, the system path is
// tried first, then ./config.yaml.
func Find(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("config file %s: %w", explicit, unwrapPathError(err))
		}
		return explicit, nil
	}
	candidates := []string{SystemPath, LocalPath}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no config file found (tried %s); create one from config.example.yaml or pass --config",
		strings.Join(candidates, " and "))
}

// unwrapPathError drops the repeated path from an *fs.PathError.
func unwrapPathError(err error) error {
	var pe interface{ Unwrap() error }
	if errors.As(err, &pe) {
		return pe.Unwrap()
	}
	return err
}

// PermissionWarning returns a warning text if the file is open to more than it
// should be, and "" if the permissions are fine. Fine means: no access for
// others and no write access for the group. Group read (0640) is allowed: the
// service runs as a user whose group owns the file.
func PermissionWarning(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	mode := info.Mode().Perm()
	if mode&0o027 == 0 {
		return ""
	}
	return fmt.Sprintf("%s is accessible by other users (mode %04o); restrict it with: chmod 640 %s (or 600)", path, mode, path)
}

// Load reads, parses and validates the config file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config file %s: %w", path, unwrapPathError(err))
	}
	cfg, err := Parse(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Parse reads a config from r, applies the defaults and validates it.
func Parse(r io.Reader) (*Config, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if err := rejectPasswordKey(data); err != nil {
		return nil, err
	}
	if err := rejectRemovedKeys(data); err != nil {
		return nil, err
	}

	cfg := Default() // defaults first: keys missing in the file keep them
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // a typo in a key is an error, not silently ignored
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// rejectPasswordKey gives a helpful message for the most likely mistake: putting
// the password itself into the file. The password is never read from there.
func rejectPasswordKey(data []byte) error {
	var raw struct {
		SMTP map[string]any `yaml:"smtp"`
	}
	if yaml.Unmarshal(data, &raw) != nil {
		return nil // syntax errors are reported by the real decode
	}
	for key := range raw.SMTP {
		if strings.EqualFold(key, "password") {
			return errors.New("smtp.password is not allowed: passwords are never read from the config file; " +
				"export the password as an environment variable and set smtp.password_env to its name")
		}
	}
	return nil
}

// rejectRemovedKeys explains keys that existed once and are gone, instead of
// the generic "field not found" of the strict parser.
func rejectRemovedKeys(data []byte) error {
	var raw struct {
		Alerts map[string]any `yaml:"alerts"`
	}
	if yaml.Unmarshal(data, &raw) != nil {
		return nil // syntax errors are reported by the real decode
	}
	if _, ok := raw.Alerts["ssh"]; ok {
		return errors.New("alerts.ssh was removed (the SSH alert is dropped); delete the ssh: block from the config")
	}
	return nil
}

// Validate checks the values that every use of the config relies on.
// It reports all problems at once, not just the first.
func (c *Config) Validate() error {
	var errs []error
	check := func(cond bool, format string, args ...any) {
		if !cond {
			errs = append(errs, fmt.Errorf(format, args...))
		}
	}

	for _, t := range []struct {
		name string
		th   Threshold
	}{
		{"alerts.cpu.process", c.Alerts.CPU.Process},
		{"alerts.cpu.total", c.Alerts.CPU.Total},
	} {
		check(validPercent(t.th.Percent), "%s.percent must be between 1 and 100, got %v", t.name, t.th.Percent)
		check(t.th.Duration > 0, "%s.duration must be positive (for example 15m), got %v", t.name, t.th.Duration)
	}
	check(validPercent(c.Alerts.Disk.Percent), "alerts.disk.percent must be between 1 and 100, got %v", c.Alerts.Disk.Percent)

	// The report settings are checked even when the report is off, so a typo
	// does not wait until someone turns it on.
	r := c.Alerts.Report
	switch strings.ToLower(strings.TrimSpace(r.Schedule)) {
	case "daily", "weekly":
	default:
		errs = append(errs, fmt.Errorf("alerts.report.schedule must be daily or weekly, got %q", r.Schedule))
	}
	if _, err := ParseWeekday(r.Weekday); err != nil {
		errs = append(errs, fmt.Errorf("alerts.report.weekday: %w", err))
	}
	if _, _, err := ParseClock(r.Time); err != nil {
		errs = append(errs, fmt.Errorf("alerts.report.time: %w", err))
	}

	return errors.Join(errs...)
}

// ParseClock reads a time of day written as HH:MM (24 hours, for example "08:00").
func ParseClock(s string) (hour, minute int, err error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil || len(strings.TrimSpace(s)) != 5 {
		return 0, 0, fmt.Errorf("must be a time like \"08:00\" (HH:MM, 24 hours), got %q", s)
	}
	return t.Hour(), t.Minute(), nil
}

// ParseWeekday reads an English weekday name, in full or as three letters,
// in any case ("monday", "Mon").
func ParseWeekday(s string) (time.Weekday, error) {
	name := strings.ToLower(strings.TrimSpace(s))
	for d := time.Sunday; d <= time.Saturday; d++ {
		full := strings.ToLower(d.String())
		if name == full || name == full[:3] {
			return d, nil
		}
	}
	return 0, fmt.Errorf("must be a weekday like \"monday\", got %q", s)
}

// ValidateSMTP checks what is needed to send mail. It is separate from Validate
// so that commands without mail (the TUI, status) work with a partial config.
func (c *Config) ValidateSMTP() error {
	var errs []error
	check := func(cond bool, format string, args ...any) {
		if !cond {
			errs = append(errs, fmt.Errorf(format, args...))
		}
	}
	s := c.SMTP
	check(strings.TrimSpace(s.Host) != "", "smtp.host is required")
	check(s.Port >= 1 && s.Port <= 65535, "smtp.port must be between 1 and 65535, got %d", s.Port)
	check(strings.TrimSpace(s.Username) != "", "smtp.username is required")
	check(strings.TrimSpace(s.PasswordEnv) != "", "smtp.password_env is required (the name of the environment variable that holds the password)")
	if s.From != "" {
		_, err := mail.ParseAddress(s.From)
		check(err == nil, "smtp.from: %q is not a valid mail address", s.From)
	}
	check(len(s.To) > 0, "smtp.to needs at least one recipient")
	for _, to := range s.To {
		_, err := mail.ParseAddress(to)
		check(err == nil, "smtp.to: %q is not a valid mail address", to)
	}
	return errors.Join(errs...)
}

// FromAddress is the sender of mails: smtp.from, or the username if unset.
func (s SMTP) FromAddress() string {
	if s.From != "" {
		return s.From
	}
	return s.Username
}

// Password reads the SMTP password from the environment variable named in
// password_env. The value is only returned, never stored or put in an error.
func (s SMTP) Password() (string, error) {
	if s.PasswordEnv == "" {
		return "", errors.New("smtp.password_env is not set")
	}
	v := os.Getenv(s.PasswordEnv)
	if v == "" {
		return "", fmt.Errorf("environment variable %s (smtp.password_env) is not set or empty; export it before running, e.g.: export %s=...",
			s.PasswordEnv, s.PasswordEnv)
	}
	return v, nil
}

func validPercent(p float64) bool { return p >= 1 && p <= 100 }
