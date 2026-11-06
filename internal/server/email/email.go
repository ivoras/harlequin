// Package email sends transactional mail (currently just registration magic
// codes) over SMTP. When SMTP is not configured the Sender falls back to logging
// the message to the server console, so the registration flow works end-to-end in
// development without any mail infrastructure.
package email

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"log"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// Config holds SMTP connection settings. A zero Host disables real sending (the
// Sender logs instead).
type Config struct {
	Host     string `yaml:"smtp_host"`
	Port     int    `yaml:"smtp_port"`
	Username string `yaml:"smtp_username"`
	// PasswordEnv names the environment variable holding the SMTP password
	// (like a provider's api_key_env); the secret itself is never stored in YAML.
	PasswordEnv string `yaml:"smtp_password_env"`
	From        string `yaml:"from"`

	// Password is resolved from PasswordEnv at config load time.
	Password string `yaml:"-"`
}

// Sender delivers mail per its Config.
type Sender struct {
	cfg Config
}

// New constructs a Sender from cfg.
func New(cfg Config) *Sender { return &Sender{cfg: cfg} }

// Configured reports whether real SMTP delivery is set up.
func (s *Sender) Configured() bool { return s != nil && strings.TrimSpace(s.cfg.Host) != "" }

// fromAddr returns the configured sender as written: "Name <addr>" or a bare
// address, defaulting to the SMTP username, then a placeholder.
func (s *Sender) fromAddr() string {
	if s.cfg.From != "" {
		return s.cfg.From
	}
	if s.cfg.Username != "" {
		return s.cfg.Username
	}
	return "harlequin@localhost"
}

// fromHeader is the From: header value — a display name is RFC 2047-encoded
// when it needs to be.
func (s *Sender) fromHeader() string {
	if a, err := mail.ParseAddress(s.fromAddr()); err == nil {
		return a.String()
	}
	return s.fromAddr()
}

// envelopeFrom is the bare address for the SMTP MAIL FROM command (which must
// not carry a display name).
func (s *Sender) envelopeFrom() string {
	if a, err := mail.ParseAddress(s.fromAddr()); err == nil {
		return a.Address
	}
	return s.fromAddr()
}

// Send delivers a plain-text message to one recipient. When SMTP is not
// configured it logs the message (subject + body) to the console and returns nil,
// so callers can rely on it succeeding in development.
func (s *Sender) Send(to, subject, body string) error {
	if !s.Configured() {
		log.Printf("email: SMTP not configured — would send to %s: %q\n%s", to, subject, body)
		return nil
	}
	return s.deliver(to, buildMessage(s.fromHeader(), to, subject, body))
}

// deliver hands a complete RFC 5322 message to the configured SMTP server.
func (s *Sender) deliver(to string, msg []byte) error {
	from := s.envelopeFrom()
	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)

	var auth smtp.Auth
	if s.cfg.Username != "" {
		auth = smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
	}

	// Port 465 is implicit TLS (SMTPS); a loopback host (a local relay like
	// postfix on the same box) is spoken to in plaintext with no STARTTLS —
	// local MTAs typically have no or self-signed certs, and the traffic never
	// leaves the machine. Everything else uses STARTTLS via smtp.SendMail
	// (which upgrades when the server advertises it).
	if s.cfg.Port == 465 {
		return s.sendTLS(addr, auth, from, to, msg)
	}
	if isLoopbackHost(s.cfg.Host) {
		return s.sendPlain(addr, auth, from, to, msg)
	}
	if err := smtp.SendMail(addr, auth, from, []string{to}, msg); err != nil {
		return fmt.Errorf("smtp send: %w", err)
	}
	return nil
}

// isLoopbackHost reports whether host names the local machine (localhost,
// 127.0.0.0/8, ::1 — bracketed IPv6 accepted).
func isLoopbackHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	h = strings.Trim(h, "[]")
	if h == "localhost" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// sendPlain delivers over a plaintext connection without attempting STARTTLS,
// for loopback relays.
func (s *Sender) sendPlain(addr string, auth smtp.Auth, from, to string, msg []byte) error {
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	defer c.Close()
	if auth != nil {
		// Only authenticate if the relay asks for it; local relays usually don't.
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(auth); err != nil {
				return fmt.Errorf("smtp auth: %w", err)
			}
		}
	}
	return transact(c, from, to, msg)
}

// sendTLS sends over an implicit-TLS connection (port 465).
func (s *Sender) sendTLS(addr string, auth smtp.Auth, from, to string, msg []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: s.cfg.Host})
	if err != nil {
		return fmt.Errorf("smtp tls dial: %w", err)
	}
	defer conn.Close()
	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	return transact(c, from, to, msg)
}

// transact runs the MAIL/RCPT/DATA/QUIT sequence on an established client.
func transact(c *smtp.Client, from, to string, msg []byte) error {
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("smtp mail: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close: %w", err)
	}
	return c.Quit()
}

// writeHeaders writes the headers every outgoing message carries: From, To,
// optional Reply-To, an RFC 2047-encoded Subject (CR/LF stripped from every
// value, so no argument can inject a header), Date, Message-ID and
// MIME-Version. The caller adds Content-Type and the body.
func writeHeaders(b *strings.Builder, from, to, replyTo, subject string) {
	hdr := func(v string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(v) }
	var id [12]byte
	_, _ = rand.Read(id[:])
	domain := "localhost"
	if a, err := mail.ParseAddress(from); err == nil {
		if at := strings.LastIndex(a.Address, "@"); at >= 0 {
			domain = a.Address[at+1:]
		}
	}
	fmt.Fprintf(b, "From: %s\r\n", hdr(from))
	fmt.Fprintf(b, "To: %s\r\n", hdr(to))
	if replyTo != "" {
		fmt.Fprintf(b, "Reply-To: %s\r\n", hdr(replyTo))
	}
	fmt.Fprintf(b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", hdr(subject)))
	fmt.Fprintf(b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(b, "Message-ID: <%s@%s>\r\n", hex.EncodeToString(id[:]), domain)
	b.WriteString("MIME-Version: 1.0\r\n")
}

// buildMessage assembles an RFC 5322 plain-text message.
func buildMessage(from, to, subject, body string) []byte {
	var b strings.Builder
	writeHeaders(&b, from, to, "", subject)
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return []byte(b.String())
}

// ValidAddress does a cheap structural check on an email address (not a delivery
// guarantee): one @, non-empty local and domain parts, a dot in the domain.
func ValidAddress(addr string) bool {
	addr = strings.TrimSpace(addr)
	at := strings.LastIndex(addr, "@")
	if at <= 0 || at == len(addr)-1 {
		return false
	}
	local, domain := addr[:at], addr[at+1:]
	if strings.ContainsAny(local, " \t\r\n") || strings.ContainsAny(domain, " \t\r\n") {
		return false
	}
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}
	if _, _, err := net.SplitHostPort(domain); err == nil {
		return false // domain shouldn't carry a port
	}
	return true
}
