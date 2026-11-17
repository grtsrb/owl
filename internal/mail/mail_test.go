package mail

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	for _, c := range []struct {
		cfg  Config
		ok   bool
		port int
		tls  string
	}{
		{Config{Host: "smtp.example.com", From: "Owl <owl@example.com>"}, true, 587, TLSStart},
		{Config{Host: "smtp.example.com", Port: 465, From: "owl@example.com"}, true, 465, TLSImplicit},
		{Config{Host: "smtp.example.com", TLS: TLSNone, From: "owl@example.com"}, false, 0, ""}, // plain to a remote server
		{Config{Host: "localhost", TLS: TLSNone, From: "owl@example.com"}, true, 25, TLSNone},
		{Config{Host: "owl-mailpit", Port: 1025, TLS: TLSNone, From: "owl@example.com"}, true, 1025, TLSNone},
		{Config{Host: "smtp.example.com", From: "not an address"}, false, 0, ""},
		{Config{Host: "smtp.example.com", From: "owl@example.com", Username: "u"}, false, 0, ""},
		{Config{Host: "smtp.example.com", From: "owl@example.com", TLS: "ssl"}, false, 0, ""},
	} {
		err := c.cfg.Validate()
		if (err == nil) != c.ok || c.ok && (c.cfg.Port != c.port || c.cfg.TLS != c.tls) {
			t.Errorf("%+v: err=%v port=%d tls=%s", c.cfg, err, c.cfg.Port, c.cfg.TLS)
		}
	}
}

func TestComposeRejectsHeaderInjection(t *testing.T) {
	s := &SMTP{Config: Config{From: "Owl <owl@example.com>"}}
	for _, m := range []Message{
		{To: "a@example.com\r\nBcc: b@example.com", Subject: "x", Body: "x"},
		{To: "a@example.com", Subject: "x\r\nBcc: b@example.com", Body: "x"},
	} {
		if _, _, err := s.compose(m); err == nil {
			t.Errorf("accepted %+v", m)
		}
	}
	raw, to, err := s.compose(Message{To: "a@example.com", Subject: "Ünïcode", Body: "line1\nline2"})
	if err != nil || to != "a@example.com" || !strings.Contains(string(raw), "Subject: =?utf-8?q?") ||
		!strings.Contains(string(raw), "\r\n\r\nline1\r\nline2\r\n") {
		t.Errorf("compose: %v %q", err, raw)
	}
}

// A server that doesn't offer STARTTLS must not get the message (or credentials) in the clear.
func TestNoSilentDowngrade(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	gotMail := make(chan bool, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		c.Write([]byte("220 fake ESMTP\r\n"))
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				gotMail <- false
				return
			}
			switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(cmd, "EHLO"):
				c.Write([]byte("250-fake\r\n250 AUTH PLAIN\r\n"))
			case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "AUTH"):
				gotMail <- true
				return
			default:
				c.Write([]byte("250 ok\r\n"))
			}
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	s := &SMTP{Config: Config{Host: "127.0.0.1", Port: port, TLS: TLSStart, From: "owl@example.com", Username: "u", Password: "p"}, Timeout: 5 * time.Second}
	if err := s.Send(context.Background(), Message{To: "a@example.com", Subject: "s", Body: "b"}); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("want STARTTLS refusal, got %v", err)
	}
	if <-gotMail {
		t.Error("credentials or mail sent without TLS")
	}
}
