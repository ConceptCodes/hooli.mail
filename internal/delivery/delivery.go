// Package delivery owns local-recipient resolution and mailbox placement.
// When SMTP ingestion receives a message, it delegates to this module rather
// than performing user lookups, mailbox selection, and app sequencing inside
// protocol code. Delivery policy (aliases, junk filtering, rejection rules,
// Sent-mail behaviour) has a home here, behind a seam the protocol adapter
// does not need to understand.
package delivery

import (
	"context"
	"fmt"

	"hooli.mail/server/internal/mailstore"
	"hooli.mail/server/internal/message"
)

// Service resolves local recipients and places messages into mailboxes. It
// holds a Store reference so the lookup-append sequence is owned in one
// place, not reassembled by each protocol adapter.
type Service struct {
	store mailstore.Store
}

func New(store mailstore.Store) *Service {
	return &Service{store: store}
}

// Result captures the outcome of delivering to a single recipient.
type Result struct {
	Recipient string
	Err       error
}

// Delivered reports whether the delivery to this recipient succeeded.
func (r Result) Delivered() bool {
	return r.Err == nil
}

// Deliver parses raw RFC 5322 bytes, resolves each recipient to a local
// user, determines mailbox placement, and stores the message. Returns one
// Result per recipient so the caller can report partial failures (e.g.
// SMTP 250 for successes, 550 for failures).
func (s *Service) Deliver(ctx context.Context, raw []byte, recipients []string) []Result {
	msg := message.Parse(raw)
	results := make([]Result, len(recipients))
	for i, rcpt := range recipients {
		results[i] = Result{Recipient: rcpt, Err: s.deliverOne(ctx, msg, rcpt)}
	}
	return results
}

// deliverOne resolves a single recipient, selects a mailbox, and appends the
// message. Currently every message lands in INBOX; this is where alias
// resolution, junk filtering, or per-user rules would hook in.
func (s *Service) deliverOne(ctx context.Context, msg message.Message, recipientEmail string) error {
	u, err := s.store.GetUserByEmail(ctx, recipientEmail)
	if err != nil {
		return fmt.Errorf("get recipient: %w", err)
	}
	if u == nil {
		return fmt.Errorf("user not found: %s", recipientEmail)
	}

	mailboxID, err := s.selectMailbox(ctx, u.ID, recipientEmail)
	if err != nil {
		return err
	}

	_, err = s.store.Append(ctx, mailboxID, mailstore.Message{
		Parsed: msg,
	})
	return err
}

// selectMailbox determines which mailbox a message should be delivered to
// for the given user. Currently always INBOX; this is the extension point
// for aliases (postmaster → INBOX or a dedicated mailbox), junk filtering,
// or server-side rules.
func (s *Service) selectMailbox(ctx context.Context, userID int64, recipientEmail string) (int64, error) {
	mb, err := s.store.GetMailboxByName(ctx, userID, "INBOX")
	if err != nil {
		return 0, fmt.Errorf("get inbox: %w", err)
	}
	if mb == nil {
		return 0, fmt.Errorf("inbox not found for user %s", recipientEmail)
	}
	return mb.ID, nil
}
