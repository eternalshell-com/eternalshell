package domains

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

func certExpiringIn(d time.Duration) Result {
	return Result{Status: 200, HasCert: true, NotAfter: now.Add(d)}
}

func TestStatusLevel(t *testing.T) {
	tests := []struct {
		r    Result
		want Level
		text string
	}{
		{Result{Status: 200}, LevelOK, "200"},
		{Result{Status: 301}, LevelOK, "301"},
		{Result{Status: 404}, LevelBad, "404"},
		{Result{Status: 503}, LevelBad, "503"},
		{Result{Err: "timeout"}, LevelBad, "timeout"},
		{Result{Err: "refused"}, LevelBad, "refused"},
		{Result{}, LevelBad, "error"}, // no status and no reason: still a problem
	}
	for _, tt := range tests {
		if got := tt.r.StatusLevel(); got != tt.want {
			t.Errorf("%+v: level = %v, want %v", tt.r, got, tt.want)
		}
		if got := tt.r.StatusText(); got != tt.text {
			t.Errorf("%+v: text = %q, want %q", tt.r, got, tt.text)
		}
	}
}

func TestCertLevelAndText(t *testing.T) {
	day := 24 * time.Hour
	tests := []struct {
		name  string
		r     Result
		level Level
		text  string
	}{
		{"plenty of time", certExpiringIn(62*day + time.Hour), LevelOK, "62 d"},
		{"14 days is fine", certExpiringIn(14*day + time.Hour), LevelOK, "14 d"},
		{"13 days warns", certExpiringIn(13*day + time.Hour), LevelWarn, "13 d"},
		{"last day", certExpiringIn(5 * time.Hour), LevelWarn, "0 d"},
		{"expired", certExpiringIn(-time.Hour), LevelBad, "invalid: expired"},
		{"invalid wins over days", Result{HasCert: true, NotAfter: now.Add(90 * day), CertErr: "unknown issuer"}, LevelBad, "invalid: unknown issuer"},
		{"no certificate", Result{Err: "refused"}, LevelNone, "-"},
	}
	for _, tt := range tests {
		if got := tt.r.CertLevel(now); got != tt.level {
			t.Errorf("%s: level = %v, want %v", tt.name, got, tt.level)
		}
		if got := tt.r.CertText(now); got != tt.text {
			t.Errorf("%s: text = %q, want %q", tt.name, got, tt.text)
		}
	}
}

func TestDaysLeft(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want int
	}{
		{48*time.Hour + time.Minute, 2},
		{25 * time.Hour, 1},
		{23 * time.Hour, 0},
		{-time.Minute, -1},
		{-25 * time.Hour, -2},
	}
	for _, tt := range tests {
		if got := certExpiringIn(tt.in).DaysLeft(now); got != tt.want {
			t.Errorf("DaysLeft(%v) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestRankOrder(t *testing.T) {
	day := 24 * time.Hour
	bad := Result{Err: "refused"}
	badCert := Result{Status: 200, HasCert: true, NotAfter: now.Add(90 * day), CertErr: "name mismatch"}
	warn := certExpiringIn(3 * day)
	ok := certExpiringIn(60 * day)

	if bad.Rank(now) != 0 || badCert.Rank(now) != 0 {
		t.Errorf("red problems come first: %d %d", bad.Rank(now), badCert.Rank(now))
	}
	if warn.Rank(now) != 1 {
		t.Errorf("yellow comes second: %d", warn.Rank(now))
	}
	if ok.Rank(now) != 2 {
		t.Errorf("fine comes last: %d", ok.Rank(now))
	}
	if (Result{Status: 404, HasCert: true, NotAfter: now.Add(3 * day)}).Rank(now) != 0 {
		t.Error("a bad status outranks a certificate warning")
	}
}

func TestConnReason(t *testing.T) {
	refused := &net.OpError{Op: "dial", Err: &net_syscall{syscall.ECONNREFUSED}}
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"dns", &net.DNSError{Err: "no such host", Name: "x.example.com"}, "dns error"},
		{"wrapped dns", fmt.Errorf("Get: %w", &net.DNSError{Err: "no such host"}), "dns error"},
		{"refused", fmt.Errorf("dial: %w", syscall.ECONNREFUSED), "refused"},
		{"refused inside an OpError", refused, "refused"},
		{"reset", fmt.Errorf("read: %w", syscall.ECONNRESET), "reset"},
		{"network timeout", &timeoutErr{}, "timeout"},
		{"tls", errors.New("remote error: tls: handshake failure"), "tls error"},
		{"other", errors.New("something odd"), "error"},
	}
	for _, tt := range tests {
		if got := connReason(tt.err); got != tt.want {
			t.Errorf("%s: connReason = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// net_syscall wraps an errno so errors.Is can see it through an OpError.
type net_syscall struct{ e syscall.Errno }

func (n *net_syscall) Error() string { return n.e.Error() }
func (n *net_syscall) Unwrap() error { return n.e }

type timeoutErr struct{}

func (*timeoutErr) Error() string   { return "i/o timeout" }
func (*timeoutErr) Timeout() bool   { return true }
func (*timeoutErr) Temporary() bool { return true }

func TestFailureReasonCountsOurDeadlineAsTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	if got := failureReason(ctx, errors.New("net/http: request canceled")); got != "timeout" {
		t.Errorf("our own deadline = %q, want timeout", got)
	}
}

func TestCertReason(t *testing.T) {
	cert := &x509.Certificate{}
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"name mismatch", x509.HostnameError{Certificate: cert, Host: "a.example.com"}, "name mismatch"},
		{"unknown issuer", x509.UnknownAuthorityError{Cert: cert}, "unknown issuer"},
		{"expired", x509.CertificateInvalidError{Cert: cert, Reason: x509.Expired}, "expired"},
		{"another invalid reason", x509.CertificateInvalidError{Cert: cert, Reason: x509.NotAuthorizedToSign}, "invalid chain"},
		{"anything else", errors.New("boom"), "invalid chain"},
	}
	for _, tt := range tests {
		if got := certReason(tt.err); got != tt.want {
			t.Errorf("%s: certReason = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestCleanText(t *testing.T) {
	if got := cleanText("https://a.example.com/\x1b[2J\n", 100); strings.ContainsAny(got, "\x1b\n") {
		t.Errorf("control characters must go: %q", got)
	}
	if got := cleanText(strings.Repeat("x", 300), 10); got != strings.Repeat("x", 10)+"…" {
		t.Errorf("long text is cut: %q", got)
	}
	if got := cleanText("/login", 100); got != "/login" {
		t.Errorf("plain text stays: %q", got)
	}
}
