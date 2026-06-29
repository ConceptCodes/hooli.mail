package message

import (
	"strings"
	"testing"
	"time"
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

	m := Parse(raw)

	if m.From != "Alice <alice@example.com>" {
		t.Fatalf("From = %q", m.From)
	}
	if len(m.To) != 2 || m.To[0] != "bob@example.com" || m.To[1] != "carol@example.com" {
		t.Fatalf("To = %v", m.To)
	}
	if m.Subject != "Hello there" {
		t.Fatalf("Subject = %q", m.Subject)
	}
	if m.Body != "This is the body.\r\nSecond line.\r\n" && !strings.HasPrefix(m.Body, "This is the body.") {
		t.Fatalf("Body = %q", m.Body)
	}
}

func TestParsePreservesRawBytesAndComputesSize(t *testing.T) {
	raw := []byte(strings.Join([]string{
		"From: a@b.com",
		"Subject: test",
		"",
		"hello",
	}, "\r\n"))

	m := Parse(raw)

	if string(m.Raw) != string(raw) {
		t.Fatalf("Raw = %q, want exact input", m.Raw)
	}
	if m.Size != len(raw) {
		t.Fatalf("Size = %d, want %d (len of raw bytes)", m.Size, len(raw))
	}
}

func TestParseExtractsFullEnvelope(t *testing.T) {
	raw := []byte(strings.Join([]string{
		"From: Alice <alice@example.com>",
		"To: bob@example.com",
		"Cc: carol@example.com, Dave <dave@example.com>",
		"Bcc: eve@example.com",
		"Reply-To: support@example.com",
		"Subject: Quarterly review",
		"Message-ID: <abc123@example.com>",
		"In-Reply-To: <parent@example.com>",
		"Date: Mon, 02 Jan 2024 15:04:05 -0700",
		"",
		"body",
	}, "\r\n"))

	m := Parse(raw)

	if len(m.Cc) != 2 || m.Cc[0] != "carol@example.com" || m.Cc[1] != "Dave <dave@example.com>" {
		t.Fatalf("Cc = %v", m.Cc)
	}
	if len(m.Bcc) != 1 || m.Bcc[0] != "eve@example.com" {
		t.Fatalf("Bcc = %v", m.Bcc)
	}
	if m.ReplyTo != "support@example.com" {
		t.Fatalf("ReplyTo = %q", m.ReplyTo)
	}
	if m.MessageID != "<abc123@example.com>" {
		t.Fatalf("MessageID = %q", m.MessageID)
	}
	if m.InReplyTo != "<parent@example.com>" {
		t.Fatalf("InReplyTo = %q", m.InReplyTo)
	}

	wantDate := time.Date(2024, 1, 2, 15, 4, 5, 0, time.FixedZone("MST", -7*3600))
	if !m.Date.Equal(wantDate) {
		t.Fatalf("Date = %v, want %v", m.Date, wantDate)
	}
}

// Folded headers (continuation lines) used to defeat inline scanners.
// net/mail handles them; this test pins that the shared parser does too.
func TestParseHandlesFoldedHeader(t *testing.T) {
	raw := []byte("Subject: A really long\r\n subject\r\n\r\nbody")
	m := Parse(raw)
	if !strings.Contains(m.Subject, "A really long") || !strings.Contains(m.Subject, "subject") {
		t.Fatalf("folded Subject = %q", m.Subject)
	}
}

func TestParseFallsBackToBodyOnGarbage(t *testing.T) {
	raw := []byte("not a message at all")
	m := Parse(raw)
	if m.Body != "not a message at all" {
		t.Fatalf("expected fallback body, got %q", m.Body)
	}
	if m.From != "" || m.Subject != "" {
		t.Fatalf("expected empty headers on fallback, got From=%q Subject=%q", m.From, m.Subject)
	}
	if m.Size != len(raw) {
		t.Fatalf("Size = %d, want %d even on fallback", m.Size, len(raw))
	}
}

// TestParseHandlesMissingOptionalHeaders confirms that a message with only
// From + body still produces a valid canonical Message without panicking.
func TestParseHandlesMissingOptionalHeaders(t *testing.T) {
	raw := []byte("From: a@b.com\r\n\r\nbody\r\n")
	m := Parse(raw)

	if m.From != "a@b.com" {
		t.Fatalf("From = %q", m.From)
	}
	if len(m.To) != 0 || len(m.Cc) != 0 || len(m.Bcc) != 0 {
		t.Fatalf("optional address fields should be empty: To=%v Cc=%v Bcc=%v", m.To, m.Cc, m.Bcc)
	}
	if m.Subject != "" || m.MessageID != "" || m.InReplyTo != "" {
		t.Fatalf("optional text fields should be empty")
	}
	if m.Body != "body\r\n" {
		t.Fatalf("Body = %q", m.Body)
	}
}
