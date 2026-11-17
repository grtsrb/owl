// Package mail sends Owl's emails (invites, password resets, test messages) over SMTP. Configuration
// comes only from the environment (OWL_SMTP_*); the password never passes through the UI or the DB.
package mail

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

// TLS modes: "tls" = implicit TLS (port 465), "starttls" = upgrade a plain connection (587, required
// to succeed), "none" = no encryption — only allowed for a server on loopback or a dev catcher.
const (
	TLSImplicit = "tls"
	TLSStart    = "starttls"
	TLSNone     = "none"
)

type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string // "Owl <owl@example.com>" or a bare address
	TLS      string
}

// Status is what the UI may show: no password, no username.
type Status struct {
	Configured bool   `json:"configured"`
	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	From       string `json:"from,omitempty"`
	TLS        string `json:"tls,omitempty"`
}

// FromEnv reads OWL_SMTP_*. No OWL_SMTP_HOST = email disabled (nil, nil).
func FromEnv() (*Config, error) {
	c := &Config{
		Host: os.Getenv("OWL_SMTP_HOST"), Username: os.Getenv("OWL_SMTP_USERNAME"), Password: os.Getenv("OWL_SMTP_PASSWORD"),
		From: os.Getenv("OWL_SMTP_FROM"), TLS: strings.ToLower(strings.TrimSpace(os.Getenv("OWL_SMTP_TLS"))),
	}
	if c.Host == "" {
		return nil, nil
	}
	if p := os.Getenv("OWL_SMTP_PORT"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("OWL_SMTP_PORT %q is not a port", p)
		}
		c.Port = n
	}
	return c, c.Validate()
}

// Validate fills defaults (TLS by port, port by TLS) and refuses unsafe combinations.
func (c *Config) Validate() error {
	if c.TLS == "" {
		c.TLS = TLSStart
		if c.Port == 465 {
			c.TLS = TLSImplicit
		}
	}
	if c.Port == 0 {
		c.Port = map[string]int{TLSImplicit: 465, TLSStart: 587, TLSNone: 25}[c.TLS]
	}
	switch c.TLS {
	case TLSImplicit, TLSStart:
	case TLSNone:
		if !isLoopbackOrDev(c.Host) {
			return fmt.Errorf("OWL_SMTP_TLS=none is only allowed for a mail server on localhost or a dev mail catcher (got %q): use tls or starttls", c.Host)
		}
	default:
		return fmt.Errorf("OWL_SMTP_TLS %q: use tls, starttls or none", c.TLS)
	}
	if strings.ContainsAny(c.Host, " \r\n/") {
		return fmt.Errorf("OWL_SMTP_HOST %q is not a host name", c.Host)
	}
	if c.From == "" {
		return errors.New("OWL_SMTP_FROM is required (e.g. \"Owl <owl@example.com>\")")
	}
	if _, err := mail.ParseAddress(c.From); err != nil {
		return fmt.Errorf("OWL_SMTP_FROM %q: %v", c.From, err)
	}
	if (c.Username == "") != (c.Password == "") {
		return errors.New("set both OWL_SMTP_USERNAME and OWL_SMTP_PASSWORD, or neither")
	}
	return nil
}

// isLoopbackOrDev: loopback, or the dev stack's Mailpit container.
func isLoopbackOrDev(host string) bool {
	if host == "localhost" || host == "mailpit" || host == "owl-mailpit" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c *Config) Status() Status {
	if c == nil {
		return Status{}
	}
	return Status{Configured: true, Host: c.Host, Port: c.Port, From: c.From, TLS: c.TLS}
}

// Message is a plain-text email to one recipient.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Sender sends messages; tests use a fake.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// SMTP is the real Sender.
type SMTP struct {
	Config
	Timeout time.Duration // whole conversation; default 20s
}

// Send delivers m. It never sends credentials over an unencrypted connection: with starttls, a server
// that doesn't offer STARTTLS is an error (no silent downgrade); net/smtp's PlainAuth also refuses
// non-TLS connections to anything but localhost.
func (s *SMTP) Send(ctx context.Context, m Message) error {
	raw, to, err := s.compose(m)
	if err != nil {
		return err
	}
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	d := &net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp connect: %w", err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	tlsCfg := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
	if s.TLS == TLSImplicit {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("smtp tls: %w", err)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	defer c.Close()
	if err := c.Hello(helloName(s.From)); err != nil {
		return fmt.Errorf("smtp hello: %w", err)
	}
	if s.TLS == TLSStart {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("smtp: server doesn't offer STARTTLS (refusing to send unencrypted; use OWL_SMTP_TLS=tls for port 465)")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}
	if s.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	from, _ := mail.ParseAddress(s.From)
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("smtp from: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp to: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp send: %w", err)
	}
	return c.Quit()
}

// helloName: the From domain (some servers reject "localhost" in EHLO).
func helloName(from string) string {
	if a, err := mail.ParseAddress(from); err == nil {
		if i := strings.LastIndexByte(a.Address, '@'); i >= 0 && i < len(a.Address)-1 {
			return a.Address[i+1:]
		}
	}
	return "localhost"
}

// compose builds the RFC 5322 message. Header values are validated/encoded so user-controlled text
// (an email address typed by an admin) can't inject headers.
func (s *SMTP) compose(m Message) ([]byte, string, error) {
	to, err := mail.ParseAddress(m.To)
	if err != nil || strings.ContainsAny(m.To, "\r\n") {
		return nil, "", fmt.Errorf("invalid recipient %q", m.To)
	}
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return nil, "", err
	}
	if strings.ContainsAny(m.Subject, "\r\n") {
		return nil, "", errors.New("invalid subject")
	}
	var id [12]byte
	_, _ = rand.Read(id[:])
	var b strings.Builder
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", from.String())
	h("To", to.String())
	h("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	h("Date", time.Now().Format(time.RFC1123Z))
	h("Message-ID", "<"+hex.EncodeToString(id[:])+"@"+helloName(s.From)+">")
	h("MIME-Version", "1.0")
	h("Content-Type", "text/plain; charset=utf-8")
	h("Content-Transfer-Encoding", "8bit")
	h("Auto-Submitted", "auto-generated")
	b.WriteString("\r\n")
	body := strings.ReplaceAll(strings.ReplaceAll(m.Body, "\r\n", "\n"), "\n", "\r\n")
	b.WriteString(body)
	if !strings.HasSuffix(body, "\r\n") {
		b.WriteString("\r\n")
	}
	return []byte(b.String()), to.Address, nil
}
