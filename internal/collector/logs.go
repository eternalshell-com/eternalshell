package collector

import (
	"bytes"
	"context"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

const (
	// logLines is how many of the newest log lines are fetched.
	logLines = 200

	// maxLogBytes is a hard limit on what is read from the Docker API, so a
	// container with enormous log lines cannot use up our memory.
	maxLogBytes = 8 << 20

	// maxLogLineRunes cuts overlong single lines (minified JSON, binary junk).
	maxLogLineRunes = 4000

	logTimeout = 5 * time.Second
)

// Logs returns the last lines of a container's output (stdout and stderr, with
// timestamps) as plain text. It is a read-only call and the text is never
// written anywhere: it only exists in memory.
func (c *Collector) Logs(id string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), logTimeout)
	defer cancel()
	return c.docker.logs(ctx, id)
}

func (d *Docker) logs(ctx context.Context, id string) (string, error) {
	if d.err != nil {
		return "", d.err
	}
	// A container with a TTY sends its output as plain text; without a TTY the
	// stream is multiplexed (stdout and stderr in framed chunks).
	info, err := d.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return "", err
	}
	tty := info.Container.Config != nil && info.Container.Config.Tty

	rc, err := d.cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: true,
		Tail:       strconv.Itoa(logLines),
		Follow:     false,
	})
	if err != nil {
		return "", err
	}
	defer rc.Close()
	return readLogs(rc, tty)
}

// readLogs turns the Docker log stream into safe plain text. stdout and stderr
// end up in one buffer in the order Docker sent them.
func readLogs(r io.Reader, tty bool) (string, error) {
	var buf bytes.Buffer
	limited := &io.LimitedReader{R: r, N: maxLogBytes}

	var err error
	if tty {
		_, err = io.Copy(&buf, limited)
	} else {
		_, err = stdcopy.StdCopy(&buf, &buf, limited)
	}
	cut := limited.N <= 0 // the limit was hit: the stream was not read to its end
	if err != nil && buf.Len() == 0 {
		return "", err
	}
	// A cut stream can end in the middle of a frame; keep what we have.

	text := sanitizeLog(buf.String())
	if cut {
		text += "\n[output truncated]\n"
	}
	return text, nil
}

var (
	// csiRe matches "ESC [ ... final-byte" (colors, cursor movement, ...).
	csiRe = regexp.MustCompile(`\x1b\[[0-9;:<=>?]*[ -/]*[@-~]`)
	// oscRe matches "ESC ] ... BEL" or "ESC ] ... ESC \" (window title, hyperlinks).
	oscRe = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)?`)
)

// sanitizeLog makes container output safe to print in a terminal. A container
// controls its own output, so it could otherwise move the cursor, change the
// window title or hide text. It removes ANSI sequences and control characters,
// keeps newlines and tabs, and shortens extremely long lines.
func sanitizeLog(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = csiRe.ReplaceAllString(s, "")
	s = oscRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			// other control characters, lone "\r" (progress bars) and any
			// ESC left over from an unknown sequence
		default:
			b.WriteRune(r)
		}
	}

	lines := strings.Split(b.String(), "\n")
	for i, line := range lines {
		if utf8.RuneCountInString(line) > maxLogLineRunes {
			lines[i] = string([]rune(line)[:maxLogLineRunes]) + "…"
		}
	}
	return strings.Join(lines, "\n")
}
