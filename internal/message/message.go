// Package message owns the canonical stored representation of a mail message.
// SMTP ingestion and IMAP APPEND both receive raw RFC 5322 bytes; this module
// is the single place that turns them into the canonical form the rest of the
// system stores, fetches, and searches. By preserving the raw wire bytes
// alongside derived envelope metadata, IMAP BODY[] responses, RFC822.SIZE,
// and header-only fetches are faithful to what was received — no information
// is silently dropped during ingestion.
package message

import (
	"bytes"
	"io"
	"net/mail"
	"strings"
	"time"
)

// Message is the canonical representation of a stored email. The Raw field
// preserves the original RFC 5322 wire form so that IMAP BODY[] responses,
// RFC822.SIZE, and header-only fetches are faithful to what was received.
// The remaining fields are derived from Raw by Parse and provide the
// envelope/body metadata that SMTP delivery, IMAP, storage, and the TUI need.
type Message struct {
	// Raw is the original RFC 5322 bytes, untouched. Stored verbatim so that
	// any consumer (IMAP BODY[], header search, size computation) can re-derive
	// what it needs without loss.
	Raw []byte

	// Envelope fields derived from headers (RFC 5322 / RFC 5321 envelope).
	From      string
	To        []string
	Cc        []string
	Bcc       []string
	ReplyTo   string
	Subject   string
	MessageID string
	InReplyTo string
	Date      time.Time

	// Body is the first decoded text part, suitable for simple display and
	// substring search. The Raw field holds the full MIME structure.
	Body string

	// Size is len(Raw) — the number of octets the message occupies on the
	// wire. This is what IMAP RFC822.SIZE must report, not just the body
	// length.
	Size int
}

// Parse decodes raw RFC 5322 bytes into a canonical Message. If raw is not a
// valid message the whole blob is preserved as Raw and Body, with envelope
// fields left zero so nothing is silently dropped.
func Parse(raw []byte) Message {
	m := Message{Raw: raw, Size: len(raw)}

	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		m.Body = string(raw)
		return m
	}

	m.Subject = strings.TrimSpace(msg.Header.Get("Subject"))
	m.MessageID = strings.TrimSpace(msg.Header.Get("Message-ID"))
	m.InReplyTo = strings.TrimSpace(msg.Header.Get("In-Reply-To"))

	if d, err := mail.ParseDate(msg.Header.Get("Date")); err == nil {
		m.Date = d
	}

	if from, err := msg.Header.AddressList("From"); err == nil && len(from) > 0 {
		m.From = formatAddress(from[0])
	}

	m.To = formatAddressList(msg.Header, "To")
	m.Cc = formatAddressList(msg.Header, "Cc")
	m.Bcc = formatAddressList(msg.Header, "Bcc")

	if rt, err := msg.Header.AddressList("Reply-To"); err == nil && len(rt) > 0 {
		m.ReplyTo = formatAddress(rt[0])
	}

	body, err := io.ReadAll(msg.Body)
	if err == nil {
		m.Body = string(body)
	}

	return m
}

// formatAddressList extracts and formats all addresses from a header field.
func formatAddressList(h mail.Header, field string) []string {
	addrs, err := h.AddressList(field)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, formatAddress(a))
	}
	return out
}

// formatAddress renders a net/mail address as "Name <addr>" when a display
// name is present, otherwise just the address.
func formatAddress(a *mail.Address) string {
	if a.Name != "" {
		return a.Name + " <" + a.Address + ">"
	}
	return a.Address
}
