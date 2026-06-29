package imap

import (
	"context"
	"sort"
	"strings"
	"testing"

	"hooli.mail/server/internal/mailstore"
	"hooli.mail/server/internal/message"
	"hooli.mail/server/internal/models"
	"hooli.mail/server/internal/storage/memory"

	imap "github.com/emersion/go-imap"
)

// flagsEqual sorts then compares two flag slices so tests are order-
// insensitive — the mailbox.ApplyFlags result comes from a map iteration.
func flagsEqual(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	for i := range g {
		if g[i] != w[i] {
			return false
		}
	}
	return true
}

// --- IMAP adapter end-to-end against the in-memory store ---

// newAuthedBackend wires up a memory store with one user (the IMAP login
// target) and returns a ready IMAPBackend.
func newAuthedBackend(t *testing.T) (*IMAPBackend, *models.User) {
	t.Helper()
	store := memory.New()
	u, err := store.CreateUser(context.Background(), "alice@hooli.test", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return NewBackend(context.Background(), store, nil), u
}

func inboxOf(t *testing.T, b *IMAPBackend, u *models.User) *models.Mailbox {
	t.Helper()
	mb, err := b.store.GetMailboxByName(context.Background(), u.ID, "INBOX")
	if err != nil || mb == nil {
		t.Fatalf("inbox lookup: %v mb=%v", err, mb)
	}
	return mb
}

// rawMessage builds RFC 5322 bytes from simple fields for test construction.
func rawMessage(from string, to []string, subject, body string) []byte {
	return []byte(strings.Join([]string{
		"From: " + from,
		"To: " + strings.Join(to, ", "),
		"Subject: " + subject,
		"",
		body,
	}, "\r\n"))
}

// TestIMAPUpdateMessagesFlagsSetAll drives the full STORE FLAGS path through
// the adapter — the same path that was silently broken before the postgres
// %w-nil fix and the applyFlagsOp fix. Uses the memory store so no Postgres is
// required. Uses UID mode so the identifier is stable from Append (SeqNum is
// only assigned when List is called).
func TestIMAPUpdateMessagesFlagsSetAll(t *testing.T) {
	t.Parallel()
	b, u := newAuthedBackend(t)
	mb := inboxOf(t, b, u)

	email, err := b.store.Append(context.Background(), mb.ID, mailstore.Message{
		Parsed: message.Parse(rawMessage("boss@hooli.test", []string{"alice@hooli.test"}, "promo", "you got it")),
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	user := &IMAPUser{backend: b, user: u, ctx: context.Background()}
	box := &IMAPMailbox{backend: b, mailbox: mb, user: user}

	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uint32(email.ID))
	if err := box.UpdateMessagesFlags(true, seqSet, imap.SetFlags,
		[]string{models.FlagSeen, models.FlagFlagged}); err != nil {
		t.Fatalf("UpdateMessagesFlags: %v", err)
	}

	got, err := b.store.List(context.Background(), mb.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d emails, want 1", len(got))
	}
	if !flagsEqual(got[0].Flags, models.FlagSeen, models.FlagFlagged) {
		t.Errorf("stored flags = %v, want [\\Seen \\Flagged]", got[0].Flags)
	}
}

// TestIMAPSearchByFlags verifies SearchMessages now honours both WithFlags
// and WithoutFlags (it previously only handled WithoutFlags).
func TestIMAPSearchByFlags(t *testing.T) {
	t.Parallel()
	b, u := newAuthedBackend(t)
	mb := inboxOf(t, b, u)

	if _, err := b.store.Append(context.Background(), mb.ID, mailstore.Message{
		Parsed: message.Parse(rawMessage("a@h", []string{"alice@hooli.test"}, "1", "1")),
	}); err != nil {
		t.Fatalf("append 1: %v", err)
	}
	seen, err := b.store.Append(context.Background(), mb.ID, mailstore.Message{
		Parsed: message.Parse(rawMessage("b@h", []string{"alice@hooli.test"}, "2", "2")),
		Flags:  []string{models.FlagSeen},
	})
	if err != nil {
		t.Fatalf("append 2: %v", err)
	}

	user := &IMAPUser{backend: b, user: u, ctx: context.Background()}
	box := &IMAPMailbox{backend: b, mailbox: mb, user: user}

	// UID search returns the email.ID, which is stable from Append.
	ids, err := box.SearchMessages(true, &imap.SearchCriteria{
		WithFlags: []string{models.FlagSeen},
	})
	if err != nil {
		t.Fatalf("search withflags: %v", err)
	}
	if len(ids) != 1 || uint32(seen.ID) != ids[0] {
		t.Errorf("WithFlags Seen search = %v, want [%d]", ids, seen.ID)
	}

	ids, err = box.SearchMessages(true, &imap.SearchCriteria{
		WithoutFlags: []string{models.FlagSeen},
	})
	if err != nil {
		t.Fatalf("search withoutflags: %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("WithoutFlags Seen search returned %d, want 1 (the unseen one)", len(ids))
	}
}

// TestIMAPSearchByText checks the substring catch-all — the previous code
// silently returned every message regardless of the Text criterion.
func TestIMAPSearchByText(t *testing.T) {
	t.Parallel()
	b, u := newAuthedBackend(t)
	mb := inboxOf(t, b, u)

	if _, err := b.store.Append(context.Background(), mb.ID, mailstore.Message{
		Parsed: message.Parse(rawMessage("alice-mentor@external.com", []string{"alice@hooli.test"}, "Quarterly review", "schedule for Q3")),
	}); err != nil {
		t.Fatalf("append 1: %v", err)
	}
	if _, err := b.store.Append(context.Background(), mb.ID, mailstore.Message{
		Parsed: message.Parse(rawMessage("noreply@spam.com", []string{"alice@hooli.test"}, "WINNER", "click here")),
	}); err != nil {
		t.Fatalf("append 2: %v", err)
	}

	user := &IMAPUser{backend: b, user: u, ctx: context.Background()}
	box := &IMAPMailbox{backend: b, mailbox: mb, user: user}

	ids, err := box.SearchMessages(true, &imap.SearchCriteria{Text: []string{"quarterly"}})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("Text quarterly matched %d, want 1", len(ids))
	}
}

// TestBuildEnvelopePreservesDisplayNamesAndMetadata pins the fix for the
// inaccurate envelope reconstruction. The old buildEnvelope naively split
// addresses on "@" and set PersonalName = mailbox part, losing display names
// and all of Cc/Message-ID/In-Reply-To.
func TestBuildEnvelopePreservesDisplayNamesAndMetadata(t *testing.T) {
	t.Parallel()
	b, u := newAuthedBackend(t)
	mb := inboxOf(t, b, u)

	raw := []byte(strings.Join([]string{
		"From: Alice <alice@example.com>",
		"To: bob@example.com, Carol <carol@example.com>",
		"Cc: dave@example.com",
		"Subject: Envelope test",
		"Message-ID: <env-123@example.com>",
		"In-Reply-To: <parent@example.com>",
		"",
		"body",
	}, "\r\n"))

	email, err := b.store.Append(context.Background(), mb.ID, mailstore.Message{
		Parsed: message.Parse(raw),
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	box := &IMAPMailbox{backend: b, mailbox: mb, user: &IMAPUser{backend: b, user: u, ctx: context.Background()}}
	env := box.buildEnvelope(*email)

	if env.Subject != "Envelope test" {
		t.Errorf("Subject = %q", env.Subject)
	}
	if len(env.From) != 1 {
		t.Fatalf("From has %d addresses, want 1", len(env.From))
	}
	if env.From[0].PersonalName != "Alice" {
		t.Errorf("From PersonalName = %q, want 'Alice'", env.From[0].PersonalName)
	}
	if env.From[0].MailboxName != "alice" || env.From[0].HostName != "example.com" {
		t.Errorf("From = %s@%s, want alice@example.com", env.From[0].MailboxName, env.From[0].HostName)
	}
	if len(env.To) != 2 {
		t.Fatalf("To has %d addresses, want 2", len(env.To))
	}
	if env.To[1].PersonalName != "Carol" {
		t.Errorf("To[1] PersonalName = %q, want 'Carol'", env.To[1].PersonalName)
	}
	if len(env.Cc) != 1 || env.Cc[0].MailboxName != "dave" {
		t.Errorf("Cc = %v, want dave@example.com", env.Cc)
	}
	if env.MessageId != "<env-123@example.com>" {
		t.Errorf("MessageId = %q", env.MessageId)
	}
	if env.InReplyTo != "<parent@example.com>" {
		t.Errorf("InReplyTo = %q", env.InReplyTo)
	}
}
