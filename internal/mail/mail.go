// Package mail sends mails through the SMTP server from the config.
//
// The connection is always encrypted, there is no plain-text fallback:
// port 465 uses implicit TLS, every other port requires STARTTLS.
package mail

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"os"
	"strings"
	"time"

	gomail "github.com/wneessen/go-mail"

	"eternalshell/internal/config"
	"eternalshell/internal/mailhtml"
)

const timeout = 15 * time.Second

// implicitTLSPort is the SMTPS port: TLS starts right at connect.
const implicitTLSPort = 465

// Message is the part of a mail the caller decides on.
type Message struct {
	Subject string
	Body    string // plain text
	HTML    string // optional: sent as an alternative part next to the plain text
}

// Mode is how the connection gets encrypted.
type Mode int

const (
	STARTTLS    Mode = iota // plain connect, then upgrade; required, never optional
	ImplicitTLS             // TLS from the first byte (port 465)
)

func (m Mode) String() string {
	if m == ImplicitTLS {
		return "implicit TLS"
	}
	return "STARTTLS"
}

// TransportFor decides the encryption from the port: 465 is implicit TLS,
// everything else (587, 25, 2525, ...) is STARTTLS.
func TransportFor(port int) Mode {
	if port == implicitTLSPort {
		return ImplicitTLS
	}
	return STARTTLS
}

// Send delivers msg to the recipients in cfg. The password is passed in by the
// caller (read from the environment) and is not kept or logged.
func Send(ctx context.Context, cfg config.SMTP, password string, msg Message) error {
	return send(ctx, cfg, password, msg)
}

// send is Send with extra client options; tests use them to trust a test certificate.
func send(ctx context.Context, cfg config.SMTP, password string, msg Message, extra ...gomail.Option) error {
	m, err := newMsg(cfg, msg)
	if err != nil {
		return err
	}

	opts := []gomail.Option{
		gomail.WithPort(cfg.Port),
		gomail.WithTimeout(timeout),
		gomail.WithSMTPAuth(gomail.SMTPAuthAutoDiscover),
		gomail.WithUsername(cfg.Username),
		gomail.WithPassword(password),
	}
	if TransportFor(cfg.Port) == ImplicitTLS {
		opts = append(opts, gomail.WithSSL())
	} else {
		// Mandatory: if the server does not offer STARTTLS, sending fails
		// instead of falling back to plain text.
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSMandatory))
	}
	opts = append(opts, extra...)

	client, err := gomail.NewClient(cfg.Host, opts...)
	if err != nil {
		return fmt.Errorf("cannot set up the mail client: %w", err)
	}
	return client.DialAndSendWithContext(ctx, m)
}

// newMsg builds the mail: addresses, subject, the plain text and, if there is
// one, the HTML as an alternative part.
func newMsg(cfg config.SMTP, msg Message) (*gomail.Msg, error) {
	m := gomail.NewMsg()
	if err := m.From(cfg.FromAddress()); err != nil {
		return nil, fmt.Errorf("invalid sender address %q: %w", cfg.FromAddress(), err)
	}
	if err := m.To(cfg.To...); err != nil {
		return nil, fmt.Errorf("invalid recipient address: %w", err)
	}
	m.Subject(msg.Subject)
	m.SetBodyString(gomail.TypeTextPlain, msg.Body)
	if msg.HTML != "" {
		// multipart/alternative: the plain text stays as it is, the HTML part
		// comes last, which is the one mail clients prefer.
		m.AddAlternativeString(gomail.TypeTextHTML, msg.HTML)
	}
	m.SetDate()
	m.SetMessageID()
	return m, nil
}

// TestMessage is the body of `eternalshell test-mail`. It contains no secrets.
func TestMessage(serverName string, now time.Time) Message {
	host, _ := os.Hostname()

	doc := mailhtml.New(serverName, now)
	doc.Badge = mailhtml.Badge{Level: mailhtml.LevelOK, Text: "Mail works"}
	doc.Title = "Test mail from EternalShell"
	doc.Sections = []mailhtml.Section{{
		Heading: "Test mail",
		Blocks: []mailhtml.Block{
			mailhtml.Facts(
				mailhtml.Fact{Label: "Server", Value: serverName},
				mailhtml.Fact{Label: "Host", Value: host},
				mailhtml.Fact{Label: "Sent", Value: now.Format(time.RFC1123), Mono: true},
			),
			mailhtml.Text("If you can read this, alert mails from this server can reach you."),
		},
	}}

	return Message{
		Subject: fmt.Sprintf("[EternalShell] %s - test mail", serverName),
		Body: fmt.Sprintf(`This is a test mail from EternalShell.

Server:  %s
Host:    %s
Sent:    %s

If you can read this, alert mails from this server can reach you.
`, serverName, host, now.Format(time.RFC1123)),
		HTML: mailhtml.Render(doc),
	}
}

// Explain turns a send error into a message that says what to check next.
// It never includes the password. Unknown errors are passed through.
func Explain(err error, cfg config.SMTP) string {
	if err == nil {
		return ""
	}
	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))
	text := err.Error()

	var dnsErr *net.DNSError
	var unknownAuth x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var certInvalid x509.CertificateInvalidError
	var smtpErr *textproto.Error

	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"

	case errors.As(err, &dnsErr):
		return fmt.Sprintf("cannot resolve the host name %q: check smtp.host and the server's DNS", cfg.Host)

	case errors.Is(err, context.DeadlineExceeded), isTimeout(err):
		return fmt.Sprintf("no answer from %s within %s: check smtp.host and smtp.port, and whether a firewall blocks outgoing connections on that port", addr, timeout)

	case strings.Contains(text, "connection refused"):
		return fmt.Sprintf("connection to %s was refused: nothing is listening on that port; check smtp.host and smtp.port", addr)

	case strings.Contains(text, "does not support STARTTLS"):
		return fmt.Sprintf("%s does not offer STARTTLS, so the connection cannot be encrypted and EternalShell will not send in plain text; "+
			"if the server uses implicit TLS, set smtp.port to 465", addr)

	case errors.As(err, &hostErr):
		return fmt.Sprintf("the server certificate does not match %q: check smtp.host (it must be the name in the certificate)", cfg.Host)

	case errors.As(err, &unknownAuth):
		return fmt.Sprintf("the certificate of %s is not trusted (unknown certificate authority or self-signed): use a server with a valid certificate", cfg.Host)

	case errors.As(err, &certInvalid):
		return fmt.Sprintf("the certificate of %s is not valid (expired or not yet valid?): %v", cfg.Host, certInvalid)

	case strings.Contains(text, "tls:") || strings.Contains(text, "first record does not look like a TLS handshake"):
		return fmt.Sprintf("TLS handshake with %s failed (%s): port 465 expects implicit TLS, other ports expect STARTTLS; check smtp.port", addr, text)

	case errors.Is(err, gomail.ErrNoSupportedAuthDiscovered):
		return fmt.Sprintf("%s offers no login method EternalShell can use after encrypting the connection; check smtp.host and smtp.port", addr)

	case errors.As(err, &smtpErr) && (smtpErr.Code == 535 || smtpErr.Code == 534 || smtpErr.Code == 530 || smtpErr.Code == 454):
		return fmt.Sprintf("login rejected by %s (%d %s): check smtp.username and the password in the environment variable %s",
			addr, smtpErr.Code, smtpErr.Msg, cfg.PasswordEnv)

	case errors.As(err, &smtpErr) && (smtpErr.Code == 550 || smtpErr.Code == 551 || smtpErr.Code == 553 || smtpErr.Code == 554):
		return fmt.Sprintf("the server refused a sender or recipient (%d %s): check smtp.from, smtp.username and smtp.to; many servers only allow the account's own address as sender",
			smtpErr.Code, smtpErr.Msg)

	case strings.Contains(text, "authentication failed") || strings.Contains(text, "Authentication"):
		return fmt.Sprintf("login rejected by %s: check smtp.username and the password in the environment variable %s: %s", addr, cfg.PasswordEnv, text)
	}
	return text
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
