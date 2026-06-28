package message

import (
	"strings"
	"testing"
)

func TestParseExtractsHeadersAndBody(t *testing.T) {
	raw := []byte(strings.Join([]string{
		"From: Alice <alice@example.com>",
		"To: bob@example.com, carol@example.com",
		"Subject: Hello there",
		"",
		"This is the body.",
		"Second line.",
	}, "\r\n"))

	p := Parse(raw)

	if p.From != "Alice <alice@example.com>" {
		t.Fatalf("From = %q", p.From)
	}
	if len(p.To) != 2 || p.To[0] != "bob@example.com" || p.To[1] != "carol@example.com" {
		t.Fatalf("To = %v", p.To)
	}
	if p.Subject != "Hello there" {
		t.Fatalf("Subject = %q", p.Subject)
	}
	if p.Body != "This is the body.\r\nSecond line.\r\n" && !strings.HasPrefix(p.Body, "This is the body.") {
		t.Fatalf("Body = %q", p.Body)
	}
}

// Folded headers (continuation lines) used to defeat both inline scanners.
// net/mail handles them; this test pins that the shared parser does too.
func TestParseHandlesFoldedHeader(t *testing.T) {
	raw := []byte("Subject: A really long\r\n subject\r\n\r\nbody")
	p := Parse(raw)
	if !strings.Contains(p.Subject, "A really long") || !strings.Contains(p.Subject, "subject") {
		t.Fatalf("folded Subject = %q", p.Subject)
	}
}

func TestParseFallsBackToBodyOnGarbage(t *testing.T) {
	raw := []byte("not a message at all")
	p := Parse(raw)
	if p.Body != "not a message at all" {
		t.Fatalf("expected fallback body, got %q", p.Body)
	}
	if p.From != "" || p.Subject != "" {
		t.Fatalf("expected empty headers on fallback, got From=%q Subject=%q", p.From, p.Subject)
	}
}
