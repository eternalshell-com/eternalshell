package collector

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"testing"

	"github.com/moby/moby/api/pkg/stdcopy"
)

// frame is one chunk of Docker's multiplexed log stream.
type frame struct {
	stream stdcopy.StdType
	text   string
}

// multiplexed builds a log stream the way dockerd sends it: every chunk starts
// with 8 header bytes (stream type, three zero bytes, payload length as a
// big-endian uint32), followed by the payload.
func multiplexed(frames ...frame) io.Reader {
	var buf bytes.Buffer
	for _, f := range frames {
		header := make([]byte, 8)
		header[0] = byte(f.stream)
		binary.BigEndian.PutUint32(header[4:], uint32(len(f.text)))
		buf.Write(header)
		buf.WriteString(f.text)
	}
	return &buf
}

func TestReadLogsMultiplexed(t *testing.T) {
	r := multiplexed(
		frame{stdcopy.Stdout, "2026-10-24T10:00:00Z hello\n"},
		frame{stdcopy.Stderr, "2026-10-24T10:00:01Z oops\n"},
		frame{stdcopy.Stdout, "2026-10-24T10:00:02Z bye\n"},
	)
	got, err := readLogs(r, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "2026-10-24T10:00:00Z hello\n2026-10-24T10:00:01Z oops\n2026-10-24T10:00:02Z bye\n"
	if got != want {
		t.Errorf("got %q, want %q (stdout and stderr in order, no frame headers)", got, want)
	}
}

func TestReadLogsTTY(t *testing.T) {
	got, err := readLogs(strings.NewReader("2026-10-24T10:00:00Z plain text\n"), true)
	if err != nil || got != "2026-10-24T10:00:00Z plain text\n" {
		t.Errorf("got (%q, %v)", got, err)
	}
}

func TestReadLogsEmpty(t *testing.T) {
	got, err := readLogs(strings.NewReader(""), false)
	if err != nil || got != "" {
		t.Errorf("got (%q, %v), want empty and no error", got, err)
	}
}

func TestSanitizeLog(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain", "hello\nworld\n", "hello\nworld\n"},
		{"colors", "\x1b[31mred\x1b[0m text", "red text"},
		{"cursor movement and clear screen", "a\x1b[2J\x1b[Hb\x1b[10;5Hc", "abc"},
		{"window title (OSC, BEL)", "x\x1b]0;evil title\x07y", "xy"},
		{"window title (OSC, ST)", "x\x1b]0;evil title\x1b\\y", "xy"},
		{"hyperlink", "\x1b]8;;http://evil\x07click\x1b]8;;\x07", "click"},
		{"carriage return progress bar", "10%\r50%\r100%\n", "10%50%100%\n"},
		{"CRLF", "a\r\nb\r\n", "a\nb\n"},
		{"tab is kept", "a\tb", "a\tb"},
		{"bell and backspace", "a\x07b\x08c", "abc"},
		{"lone ESC", "a\x1bb", "ab"},
		{"C1 control", "a\u009bb", "ab"},
		{"invalid UTF-8", "a\xffb", "a�b"},
		{"umlauts survive", "Größe ✓", "Größe ✓"},
	}
	for _, tt := range tests {
		if got := sanitizeLog(tt.in); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestSanitizeLogShortensLongLines(t *testing.T) {
	got := sanitizeLog(strings.Repeat("x", maxLogLineRunes+500) + "\nshort\n")
	lines := strings.Split(got, "\n")
	if len([]rune(lines[0])) != maxLogLineRunes+1 || !strings.HasSuffix(lines[0], "…") {
		t.Errorf("long line not shortened: %d runes", len([]rune(lines[0])))
	}
	if lines[1] != "short" {
		t.Errorf("next line damaged: %q", lines[1])
	}
}
