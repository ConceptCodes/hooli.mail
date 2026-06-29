package delivery

import (
	"context"
	"strings"
	"testing"

	"hooli.mail/server/internal/storage/memory"
)

func newService(t *testing.T) (*Service, *memory.Store) {
	t.Helper()
	store := memory.New()
	return New(store), store
}

func mustCreateUser(t *testing.T, store *memory.Store, email string) {
	t.Helper()
	if _, err := store.CreateUser(context.Background(), email, "hash"); err != nil {
		t.Fatalf("create user %s: %v", email, err)
	}
}

func rawMsg(from, to, subject, body string) []byte {
	return []byte(strings.Join([]string{
		"From: " + from,
		"To: " + to,
		"Subject: " + subject,
		"",
		body,
	}, "\r\n"))
}

// TestDeliverToKnownRecipient verifies the happy path: a message addressed to
// a local user lands in their INBOX with the correct content.
func TestDeliverToKnownRecipient(t *testing.T) {
	svc, store := newService(t)
	mustCreateUser(t, store, "alice@hooli.test")

	raw := rawMsg("stranger@external.com", "alice@hooli.test", "hello", "world")

	results := svc.Deliver(context.Background(), raw, []string{"alice@hooli.test"})

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if !results[0].Delivered() {
		t.Fatalf("delivery failed: %v", results[0].Err)
	}

	mb, err := store.GetMailboxByName(context.Background(), 1, "INBOX")
	if err != nil || mb == nil {
		t.Fatalf("inbox: %v %v", err, mb)
	}
	emails, err := store.List(context.Background(), mb.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(emails) != 1 {
		t.Fatalf("inbox has %d emails, want 1", len(emails))
	}
	if !strings.Contains(emails[0].Body, "world") {
		t.Errorf("body = %q, want it to contain 'world'", emails[0].Body)
	}
	if emails[0].From != "stranger@external.com" {
		t.Errorf("from = %q", emails[0].From)
	}
}

// TestDeliverToUnknownRecipient verifies that delivery fails for a recipient
// that is not a local user.
func TestDeliverToUnknownRecipient(t *testing.T) {
	svc, _ := newService(t)
	// No users created.

	raw := rawMsg("stranger@external.com", "ghost@hooli.test", "hello", "world")

	results := svc.Deliver(context.Background(), raw, []string{"ghost@hooli.test"})

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Delivered() {
		t.Fatal("delivery to unknown recipient succeeded; want failure")
	}
}

// TestDeliverPartialFailure verifies that when delivering to multiple
// recipients, success and failure are reported independently.
func TestDeliverPartialFailure(t *testing.T) {
	svc, store := newService(t)
	mustCreateUser(t, store, "alice@hooli.test")
	// bob@hooli.test does not exist

	raw := rawMsg("stranger@external.com", "alice@hooli.test, ghost@hooli.test", "hello", "world")

	results := svc.Deliver(context.Background(), raw, []string{"alice@hooli.test", "ghost@hooli.test"})

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	delivered := 0
	failed := 0
	for _, r := range results {
		if r.Delivered() {
			delivered++
		} else {
			failed++
		}
	}
	if delivered != 1 {
		t.Errorf("delivered = %d, want 1", delivered)
	}
	if failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
}

// TestDeliverPreservesCanonicalMessage verifies that the canonical message
// representation survives the delivery round trip: raw bytes, derived
// envelope fields, and size all reach storage intact.
func TestDeliverPreservesCanonicalMessage(t *testing.T) {
	svc, store := newService(t)
	mustCreateUser(t, store, "alice@hooli.test")

	raw := []byte(strings.Join([]string{
		"From: Alice <alice@external.com>",
		"To: alice@hooli.test",
		"Cc: bob@external.com",
		"Subject: Delivery test",
		"Message-ID: <dt-456@external.com>",
		"",
		"body content",
	}, "\r\n"))

	results := svc.Deliver(context.Background(), raw, []string{"alice@hooli.test"})
	if !results[0].Delivered() {
		t.Fatalf("delivery failed: %v", results[0].Err)
	}

	mb, _ := store.GetMailboxByName(context.Background(), 1, "INBOX")
	emails, _ := store.List(context.Background(), mb.ID)
	if len(emails) != 1 {
		t.Fatalf("got %d emails, want 1", len(emails))
	}
	e := emails[0]

	if e.Size != len(raw) {
		t.Errorf("Size = %d, want %d", e.Size, len(raw))
	}
	if e.MessageID != "<dt-456@external.com>" {
		t.Errorf("MessageID = %q", e.MessageID)
	}
	if len(e.Cc) != 1 || e.Cc[0] != "bob@external.com" {
		t.Errorf("Cc = %v", e.Cc)
	}
	if e.From != "Alice <alice@external.com>" {
		t.Errorf("From = %q", e.From)
	}
}
