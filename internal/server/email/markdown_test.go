package email

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strings"
	"testing"
)

const sample = "# Izvještaj\n\nŠto je novo: **važno** i [link](https://example.com).\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n<script>alert(1)</script>\n\n[x](javascript:alert(1))\n"

func parse(t *testing.T, raw []byte) (*mail.Message, map[string]string) {
	t.Helper()
	m, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	mt, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/alternative" {
		t.Fatalf("content type %q: %v", mt, err)
	}
	parts := map[string]string{}
	mr := multipart.NewReader(m.Body, params["boundary"])
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(quotedprintable.NewReader(p))
		ct, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
		parts[ct] = string(b)
	}
	return m, parts
}

func TestBuildMarkdownMessage(t *testing.T) {
	raw, err := BuildMarkdownMessage("Harlequin <h@example.org>", "to@example.com", "me@example.com", "Izvještaj: šđčćž", sample)
	if err != nil {
		t.Fatal(err)
	}
	m, parts := parse(t, raw)

	if got, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject")); got != "Izvještaj: šđčćž" {
		t.Errorf("subject round trip = %q", got)
	}
	if m.Header.Get("Reply-To") != "me@example.com" || m.Header.Get("Message-ID") == "" || m.Header.Get("Date") == "" {
		t.Errorf("missing headers: %v", m.Header)
	}
	if parts["text/plain"] != strings.ReplaceAll(sample, "\n", "\r\n") {
		t.Errorf("text part must be the Markdown source, got %q", parts["text/plain"])
	}
	h := parts["text/html"]
	for _, want := range []string{"<h1>Izvještaj</h1>", "<strong>važno</strong>", "<table>", "<th>a</th>", `href="https://example.com"`, "<style>", "charset=\"utf-8\""} {
		if !strings.Contains(h, want) {
			t.Errorf("html missing %q", want)
		}
	}
	if strings.Contains(h, "<script>") || strings.Contains(h, "javascript:") {
		t.Errorf("html must not carry scripts or javascript: URLs:\n%s", h)
	}
	// text/plain first, text/html last (clients prefer the last alternative).
	if i, j := strings.Index(string(raw), "text/plain"), strings.Index(string(raw), "text/html"); i < 0 || j < i {
		t.Error("part order must be plain then html")
	}
}

func TestHeaderInjectionAndRecipient(t *testing.T) {
	raw, _ := BuildMarkdownMessage("h@example.org", "to@example.com", "", "hi\r\nBcc: evil@example.com", "x")
	m, _ := parse(t, raw)
	if m.Header.Get("Bcc") != "" {
		t.Error("CRLF in the subject injected a header")
	}
	for _, bad := range []string{"", "a@b", "a@b.com, c@d.com", "Name <a@b.com>", "a@b.com\r\nBcc: x@y.com", "no-at-sign"} {
		if _, err := ParseRecipient(bad); err == nil {
			t.Errorf("ParseRecipient(%q) must fail", bad)
		}
	}
	if a, err := ParseRecipient(" ok@example.com "); err != nil || a != "ok@example.com" {
		t.Errorf("valid address rejected: %v", err)
	}
	if err := New(Config{}).SendMarkdown("a@b.com", "", "s", "m"); err != ErrNotConfigured {
		t.Errorf("unconfigured sender must return ErrNotConfigured, got %v", err)
	}
}

// End to end through a fake SMTP relay: SendMarkdown delivers one multipart message.
func TestSendMarkdownToRelay(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var got strings.Builder
	done := make(chan struct{})
	go func() { defer close(done); fakeSMTP(t, ln, &got) }()
	s := New(Config{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, From: "harlequin@example.com"})
	if err := s.SendMarkdown("user@example.com", "me@example.com", "Hello", "# Hi\n\nthere"); err != nil {
		t.Fatal(err)
	}
	<-done
	_, parts := parse(t, []byte(got.String()))
	if !strings.Contains(parts["text/plain"], "# Hi") || !strings.Contains(parts["text/html"], "<h1>Hi</h1>") {
		t.Errorf("delivered message lacks a part: %v", parts)
	}
}

func TestFromWithDisplayName(t *testing.T) {
	s := New(Config{Host: "x", From: "Example Harlequin <harlequin@harlequin.example.com>"})
	if got := s.envelopeFrom(); got != "harlequin@harlequin.example.com" {
		t.Errorf("envelope sender must be the bare address, got %q", got)
	}
	if got := s.fromHeader(); got != `"Example Harlequin" <harlequin@harlequin.example.com>` {
		t.Errorf("From header = %q", got)
	}
	raw, _ := BuildMarkdownMessage(s.fromHeader(), "to@example.com", "", "s", "m")
	m, _ := parse(t, raw)
	if id := m.Header.Get("Message-ID"); !strings.HasSuffix(id, "@harlequin.example.com>") {
		t.Errorf("Message-ID should use the sender's domain, got %q", id)
	}
	// A bare configured address still works unchanged.
	if b := New(Config{Host: "x", From: "a@b.hr"}); b.envelopeFrom() != "a@b.hr" || b.fromHeader() != "<a@b.hr>" && b.fromHeader() != "a@b.hr" {
		t.Errorf("bare address: envelope %q header %q", b.envelopeFrom(), b.fromHeader())
	}
}

// With attachments the message is multipart/mixed: the Markdown/HTML body
// first, then each file, base64-encoded under its (possibly non-ASCII) name.
func TestBuildMarkdownMessageWithAttachments(t *testing.T) {
	pdf := append([]byte("%PDF-1.4 "), bytes.Repeat([]byte{0, 0xff, 'x'}, 300)...)
	raw, err := BuildMarkdownMessage("h@example.org", "to@example.com", "", "Plaća", "# Hi\n\nu privitku",
		Attachment{Name: "Platna lista Đurđica.pdf", Mime: "application/pdf", Data: pdf},
		Attachment{Name: "../evil\r\nBcc: x@example.com.csv", Data: []byte("a;b\n")})
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if m.Header.Get("Bcc") != "" {
		t.Fatal("an attachment name injected a header")
	}
	mt, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/mixed" {
		t.Fatalf("content type %q (%v)", mt, err)
	}
	mr := multipart.NewReader(m.Body, params["boundary"])
	body, err := mr.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if bt, bp, _ := mime.ParseMediaType(body.Header.Get("Content-Type")); bt != "multipart/alternative" {
		t.Fatalf("first part is %q", bt)
	} else {
		inner := multipart.NewReader(body, bp["boundary"])
		n := 0
		for {
			p, err := inner.NextPart()
			if err != nil {
				break
			}
			n++
			if b, _ := io.ReadAll(p); !strings.Contains(string(b), "u privitku") {
				t.Errorf("body part %d lacks the text", n)
			}
		}
		if n != 2 {
			t.Errorf("%d body parts, want text + html", n)
		}
	}
	var names []string
	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}
		names = append(names, p.FileName())
		data, _ := io.ReadAll(base64.NewDecoder(base64.StdEncoding, p))
		if p.FileName() == "Platna lista Đurđica.pdf" && (!bytes.Equal(data, pdf) || p.Header.Get("Content-Type") != "application/pdf") {
			t.Errorf("PDF attachment did not round-trip (%d bytes, %q)", len(data), p.Header.Get("Content-Type"))
		}
	}
	if len(names) != 2 || names[0] != "Platna lista Đurđica.pdf" || strings.ContainsAny(names[1], "/\r\n") {
		t.Errorf("attachment names %q", names)
	}
	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 998 {
			t.Fatalf("a line of %d bytes exceeds the SMTP limit", len(line))
		}
	}
}
