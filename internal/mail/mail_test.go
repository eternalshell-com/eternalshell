package mail

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"eternalshell/internal/config"
)

func TestTransportFor(t *testing.T) {
	tests := []struct {
		port int
		want Mode
	}{
		{465, ImplicitTLS},
		{587, STARTTLS},
		{25, STARTTLS},
		{2525, STARTTLS},
		{0, STARTTLS},
	}
	for _, tt := range tests {
		if got := TransportFor(tt.port); got != tt.want {
			t.Errorf("TransportFor(%d) = %v, want %v", tt.port, got, tt.want)
		}
	}
	if ImplicitTLS.String() != "implicit TLS" || STARTTLS.String() != "STARTTLS" {
		t.Error("mode labels changed")
	}
}

func TestExplain(t *testing.T) {
	cfg := config.SMTP{Host: "smtp.example.com", Port: 587, PasswordEnv: "ES_PW"}

	tests := []struct {
		name string
		err  error
		want string
	}{
		{"dns", &net.DNSError{Err: "no such host", Name: "smtp.example.com"}, "cannot resolve"},
		{"timeout", context.DeadlineExceeded, "no answer from smtp.example.com:587"},
		{"refused", errors.New("dial tcp 1.2.3.4:587: connect: connection refused"), "was refused"},
		{"no starttls", errors.New(`STARTTLS mode set to: "TLSMandatory", but target host does not support STARTTLS`), "will not send in plain text"},
		{"hostname mismatch", x509.HostnameError{Certificate: &x509.Certificate{}, Host: "smtp.example.com"}, "does not match"},
		{"untrusted cert", x509.UnknownAuthorityError{}, "not trusted"},
		{"bad login", &textproto.Error{Code: 535, Msg: "5.7.8 bad credentials"}, "environment variable ES_PW"},
		{"refused sender", &textproto.Error{Code: 553, Msg: "sender not allowed"}, "smtp.from"},
		{"wrapped", fmt.Errorf("send failed: %w", &net.DNSError{Err: "no such host"}), "cannot resolve"},
		{"unknown passes through", errors.New("something odd"), "something odd"},
	}
	for _, tt := range tests {
		got := Explain(tt.err, cfg)
		if !strings.Contains(got, tt.want) {
			t.Errorf("%s: Explain = %q, want it to contain %q", tt.name, got, tt.want)
		}
	}
	if Explain(nil, cfg) != "" {
		t.Error("Explain(nil) should be empty")
	}
}

func TestTestMessage(t *testing.T) {
	m := TestMessage("main-vps", time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC))
	if m.Subject != "[EternalShell] main-vps - test mail" {
		t.Errorf("subject = %q", m.Subject)
	}
	if !strings.Contains(m.Body, "main-vps") {
		t.Errorf("body should name the server:\n%s", m.Body)
	}
}
