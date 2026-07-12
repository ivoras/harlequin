package email

import (
	"bufio"
	"net"
	"strings"
	"testing"
)

func TestIsLoopbackHost(t *testing.T) {
	t.Parallel()
	for _, h := range []string{"localhost", "LOCALHOST", "127.0.0.1", "127.0.0.53", "::1", "[::1]"} {
		if !isLoopbackHost(h) {
			t.Errorf("isLoopbackHost(%q) = false, want true", h)
		}
	}
	for _, h := range []string{"smtp.gmail.com", "192.168.1.10", "example.com", ""} {
		if isLoopbackHost(h) {
			t.Errorf("isLoopbackHost(%q) = true, want false", h)
		}
	}
}

// fakeSMTP is a minimal plaintext SMTP server that fails the test if the
// client ever attempts STARTTLS, and records the DATA payload.
func fakeSMTP(t *testing.T, ln net.Listener, got *strings.Builder) {
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	w("220 fake ESMTP")
	inData := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if inData {
			if line == "." {
				inData = false
				w("250 ok")
				continue
			}
			got.WriteString(line + "\n")
			continue
		}
		switch {
		case strings.HasPrefix(strings.ToUpper(line), "EHLO"), strings.HasPrefix(strings.ToUpper(line), "HELO"):
			// Advertise nothing beyond the basics — no STARTTLS, no AUTH.
			w("250-fake")
			w("250 8BITMIME")
		case strings.HasPrefix(strings.ToUpper(line), "STARTTLS"):
			t.Error("client attempted STARTTLS against a loopback relay")
			w("454 no")
		case strings.HasPrefix(strings.ToUpper(line), "MAIL"), strings.HasPrefix(strings.ToUpper(line), "RCPT"):
			w("250 ok")
		case strings.ToUpper(line) == "DATA":
			inData = true
			w("354 go")
		case strings.ToUpper(line) == "QUIT":
			w("221 bye")
			return
		default:
			w("250 ok")
		}
	}
}

func TestSendPlainToLoopbackRelay(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var got strings.Builder
	done := make(chan struct{})
	go func() { defer close(done); fakeSMTP(t, ln, &got) }()

	port := ln.Addr().(*net.TCPAddr).Port
	s := New(Config{Host: "127.0.0.1", Port: port, From: "harlequin@example.com"})
	if err := s.Send("user@example.com", "Test code", "Your code is 123456."); err != nil {
		t.Fatalf("Send: %v", err)
	}
	<-done
	if !strings.Contains(got.String(), "Your code is 123456.") {
		t.Fatalf("body not delivered; got:\n%s", got.String())
	}
	if !strings.Contains(got.String(), "Subject: Test code") {
		t.Fatalf("subject missing; got:\n%s", got.String())
	}
}
