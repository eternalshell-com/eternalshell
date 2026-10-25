package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// stateVersion changes when the file format changes; an older file is then
// treated as "no previous report".
const stateVersion = 1

// MountState is the use of one mount at the time of a report.
type MountState struct {
	Used  uint64 `json:"used"`
	Total uint64 `json:"total"`
}

// State is the small snapshot saved after each report, so the next one can show
// what changed.
type State struct {
	Version  int       `json:"version"`
	Time     time.Time `json:"time"`      // when the report was made
	BootTime time.Time `json:"boot_time"` // when the machine booted

	Mounts map[string]MountState `json:"mounts,omitempty"`

	// DiskKnown says whether the Docker disk usage was measured; Projects holds
	// its totals by projectKey, and Reclaimable the space that could be freed.
	DiskKnown   bool             `json:"disk_known"`
	Projects    map[string]int64 `json:"projects,omitempty"`
	Reclaimable int64            `json:"reclaimable"`

	// Containers lists the container names by compose project (standaloneGroup
	// for containers without one).
	Containers map[string][]string `json:"containers,omitempty"`
}

const standaloneGroup = "(standalone)"

// projectKey / containerKey are the keys of State.Projects: a compose project
// and a standalone container can have the same name.
func projectKey(name string) string   { return "project:" + name }
func containerKey(name string) string { return "container:" + name }

// keyLabel turns such a key back into text for the report.
func keyLabel(key string) string {
	if name, ok := strings.CutPrefix(key, "container:"); ok {
		return name + " (container)"
	}
	return strings.TrimPrefix(key, "project:")
}

// StateDir is where the state is kept: $STATE_DIRECTORY (systemd sets it for the
// service), else $XDG_STATE_HOME/eternalshell, else ~/.local/state/eternalshell.
// It returns "" if there is no usable place.
func StateDir() string {
	if v := os.Getenv("STATE_DIRECTORY"); v != "" {
		first, _, _ := strings.Cut(v, ":") // systemd may list several, separated by ":"
		return first
	}
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "eternalshell")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "state", "eternalshell")
	}
	return ""
}

// StatePath is the state file.
func StatePath() string {
	dir := StateDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "report.json")
}

// LoadState reads the saved state. It returns nil (and no error) when there is
// none, which is the normal case before the first report. A file that cannot be
// read or understood returns nil and an error, which the caller should only warn
// about: a broken file must not stop a report.
func LoadState() (*State, error) {
	path := StatePath()
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", path, err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s is not valid: %w", path, err)
	}
	if s.Version != stateVersion {
		return nil, fmt.Errorf("%s has format version %d, expected %d; ignoring it", path, s.Version, stateVersion)
	}
	return &s, nil
}

// Save writes the state atomically (a temporary file, then a rename), readable
// by the owner only.
func (s State) Save() error {
	dir := StateDir()
	if dir == "" {
		return errors.New("no state directory: set STATE_DIRECTORY or HOME")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cannot create %s: %w", dir, err)
	}
	s.Version = stateVersion
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "report-*.tmp")
	if err != nil {
		return fmt.Errorf("cannot write in %s: %w", dir, err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), StatePath())
}
