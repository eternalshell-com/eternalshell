package domains

import (
	"crypto/x509"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"syscall"
	"time"
	"unicode"
)

// warnDays is the number of days left from which a certificate is a warning:
// under 14 days is yellow.
const warnDays = 14

// Result is what one check found out about a host.
type Result struct {
	Host      string
	CheckedAt time.Time

	Status   int    // HTTP status code; 0 if the request failed
	Err      string // short reason if the request failed ("timeout", "refused", ...)
	Location string // Location header of a 3xx response, cleaned

	HasCert  bool      // a certificate was received
	NotAfter time.Time // expiry of the leaf certificate
	CertErr  string    // short reason if the certificate is invalid; "" if it is valid
}

// Level says how serious something is. The order matters: higher is worse.
type Level int

const (
	LevelNone Level = iota // nothing to say (for example no certificate)
	LevelOK
	LevelWarn // yellow
	LevelBad  // red
)

// StatusLevel: 1xx, 2xx and 3xx are fine, 4xx, 5xx and failed requests are bad.
func (r Result) StatusLevel() Level {
	switch {
	case r.Err != "" || r.Status == 0 || r.Status >= 400:
		return LevelBad
	default:
		return LevelOK
	}
}

// StatusText is the STATUS column: the code, or the short reason of a failure.
func (r Result) StatusText() string {
	if r.Err != "" || r.Status == 0 {
		if r.Err == "" {
			return "error"
		}
		return r.Err
	}
	return fmt.Sprint(r.Status)
}

// DaysLeft is the number of whole days until the certificate expires; negative
// once it has expired.
func (r Result) DaysLeft(now time.Time) int {
	return int(math.Floor(r.NotAfter.Sub(now).Hours() / 24)) // expired: -1 and below
}

// CertLevel: bad if the certificate is invalid or expired, a warning when fewer
// than warnDays days are left, otherwise fine. No certificate gives LevelNone.
func (r Result) CertLevel(now time.Time) Level {
	switch {
	case !r.HasCert:
		return LevelNone
	case r.CertErr != "" || r.DaysLeft(now) < 0:
		return LevelBad
	case r.DaysLeft(now) < warnDays:
		return LevelWarn
	default:
		return LevelOK
	}
}

// CertText is the CERT column: days left, or why the certificate is invalid.
func (r Result) CertText(now time.Time) string {
	switch {
	case !r.HasCert:
		return "-"
	case r.CertErr != "":
		return "invalid: " + r.CertErr
	case r.DaysLeft(now) < 0:
		return "invalid: expired"
	default:
		return fmt.Sprintf("%d d", r.DaysLeft(now))
	}
}

// Rank orders the list: problems (red) first, then warnings (yellow), then the
// rest. Inside a rank the caller sorts alphabetically.
func (r Result) Rank(now time.Time) int {
	worst := max(r.StatusLevel(), r.CertLevel(now))
	switch worst {
	case LevelBad:
		return 0
	case LevelWarn:
		return 1
	default:
		return 2
	}
}

// connReason turns the error of a failed request into a short word for the
// STATUS column.
func connReason(err error) string {
	var dnsErr *net.DNSError
	var netErr net.Error
	text := err.Error()
	switch {
	case errors.As(err, &dnsErr):
		return "dns error"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused"
	case errors.Is(err, syscall.ECONNRESET):
		return "reset"
	case errors.As(err, &netErr) && netErr.Timeout(), errors.Is(err, errTimeout):
		return "timeout"
	case strings.Contains(text, "tls:") || strings.Contains(text, "TLS handshake"):
		return "tls error"
	default:
		return "error"
	}
}

var errTimeout = errors.New("timeout")

// certReason turns the error of a failed certificate check into a short reason.
func certReason(err error) string {
	var invalid x509.CertificateInvalidError
	var host x509.HostnameError
	var unknown x509.UnknownAuthorityError
	switch {
	case errors.As(err, &host):
		return "name mismatch"
	case errors.As(err, &unknown):
		return "unknown issuer"
	case errors.As(err, &invalid):
		switch invalid.Reason {
		case x509.Expired:
			return "expired"
		default:
			return "invalid chain"
		}
	default:
		return "invalid chain"
	}
}

// cleanText removes control characters from text that came from a remote
// server (a Location header) and cuts it to max runes, so it cannot move the
// cursor or fill the screen.
func cleanText(s string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) {
			r = '?'
		}
		if n == max {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}
