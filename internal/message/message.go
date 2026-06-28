// Package message parses the RFC 5322 wire form of a mail message into the
// domain fields the rest of the system needs. SMTP ingestion and IMAP APPEND
// both receive raw message bytes; this module is the single place that knows
// how to turn them into From / To / Subject / Body, so a header-folding or
// multi-header bug only has to be fixed once.
package message

import (
	"bytes"
	"io"
	"net/mail"
	"strings"
)

// Parsed is the domain form of a raw message.
type Parsed struct {
	From    string
	To      []string
	Subject string
	Body    string
}

// Parse decodes raw using net/mail (which handles header folding, quoted
// printables in address lists, and the header/body split) and returns the
// fields callers actually use. If raw is not a valid message the whole blob is
// returned as Body so nothing is silently dropped.
func Parse(raw []byte) Parsed {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return Parsed{Body: string(raw)}
	}

	p := Parsed{
		Subject: strings.TrimSpace(m.Header.Get("Subject")),
	}

	if from, err := m.Header.AddressList("From"); err == nil && len(from) > 0 {
		p.From = formatAddress(from[0])
	}

	if to, err := m.Header.AddressList("To"); err == nil {
		for _, a := range to {
			p.To = append(p.To, formatAddress(a))
		}
	}

	body, err := io.ReadAll(m.Body)
	if err == nil {
		p.Body = string(body)
	} else {
		p.Body = ""
	}
	return p
}

// formatAddress renders a net/mail address as "Name <addr>" when a display
// name is present, otherwise just the address.
func formatAddress(a *mail.Address) string {
	if a.Name != "" {
		return a.Name + " <" + a.Address + ">"
	}
	return a.Address
}
