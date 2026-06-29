package memory

import (
	"context"
	"strings"
	"testing"

	"hooli.mail/server/internal/mailstore"
	"hooli.mail/server/internal/message"
	"hooli.mail/server/internal/models"
)

func newTestUser(t *testing.T, s *Store) *models.User {
	t.Helper()
	u, err := s.CreateUser(context.Background(), "user@x.com", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

func inboxOf(t *testing.T, s *Store, userID int64) *models.Mailbox {
	t.Helper()
	mb, err := s.GetMailboxByName(context.Background(), userID, "INBOX")
	if err != nil {
		t.Fatalf("get inbox: %v", err)
	}
	if mb == nil {
		t.Fatal("inbox not found")
	}
	return mb
}

func appendMsg(t *testing.T, s *Store, mailboxID int64, flags []string) *models.Email {
	t.Helper()
	raw := []byte(strings.Join([]string{
		"From: f@x.com",
		"To: user@x.com",
		"Subject: s",
		"",
		"b",
	}, "\r\n"))
	e, err := s.Append(context.Background(), mailboxID, mailstore.Message{
		Parsed: message.Parse(raw),
		Flags:  flags,
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	return e
}

// Status previously reported Recent == Unseen == Messages for every message
// forever. It must now derive each from the actual flags.
func TestStatusDerivesCountsFromFlags(t *testing.T) {
	s := New()
	u := newTestUser(t, s)
	mb := inboxOf(t, s, u.ID)

	appendMsg(t, s, mb.ID, nil)                                          // recent + unseen (default \Recent, no \Seen)
	appendMsg(t, s, mb.ID, []string{models.FlagSeen})                    // seen
	appendMsg(t, s, mb.ID, []string{models.FlagRecent, models.FlagSeen}) // recent but seen

	st, err := s.Status(context.Background(), mb.ID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Messages != 3 {
		t.Errorf("Messages = %d, want 3", st.Messages)
	}
	if st.Recent != 2 {
		t.Errorf("Recent = %d, want 2 (only \\Recent-flagged)", st.Recent)
	}
	if st.Unseen != 1 {
		t.Errorf("Unseen = %d, want 1 (only the one without \\Seen)", st.Unseen)
	}
	if st.UIDValidity == 0 {
		t.Error("UIDValidity should be non-zero")
	}
}

// Expunge must delete only \Deleted messages and report how many.
func TestExpungeDeletesOnlyDeleted(t *testing.T) {
	s := New()
	u := newTestUser(t, s)
	mb := inboxOf(t, s, u.ID)

	keep := appendMsg(t, s, mb.ID, nil)
	del := appendMsg(t, s, mb.ID, []string{models.FlagDeleted})
	_ = keep
	_ = del

	n, err := s.Expunge(context.Background(), mb.ID)
	if err != nil {
		t.Fatalf("expunge: %v", err)
	}
	if n != 1 {
		t.Fatalf("expunged = %d, want 1", n)
	}

	left, err := s.List(context.Background(), mb.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(left) != 1 {
		t.Fatalf("remaining = %d, want 1", len(left))
	}
}

// Copy previously dropped flags because it routed through a create that
// hard-coded [\Recent]. It must now preserve source flags.
func TestCopyPreservesFlags(t *testing.T) {
	s := New()
	u := newTestUser(t, s)
	inbox := inboxOf(t, s, u.ID)
	src := appendMsg(t, s, inbox.ID, []string{models.FlagSeen, models.FlagFlagged})

	dest, err := s.CreateMailbox(context.Background(), u.ID, "Archive")
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}

	if err := s.Copy(context.Background(), inbox.ID, []int64{src.ID}, dest.ID); err != nil {
		t.Fatalf("copy: %v", err)
	}

	copied, err := s.List(context.Background(), dest.ID)
	if err != nil {
		t.Fatalf("list dest: %v", err)
	}
	if len(copied) != 1 {
		t.Fatalf("dest has %d, want 1", len(copied))
	}
	for _, f := range []string{models.FlagSeen, models.FlagFlagged} {
		found := false
		for _, have := range copied[0].Flags {
			if have == f {
				found = true
			}
		}
		if !found {
			t.Errorf("copied message missing flag %q; flags=%v", f, copied[0].Flags)
		}
	}
}

// Append with no flags defaults to [\Recent] (the delivery invariant).
func TestAppendDefaultsToRecent(t *testing.T) {
	s := New()
	u := newTestUser(t, s)
	mb := inboxOf(t, s, u.ID)

	e := appendMsg(t, s, mb.ID, nil)
	if len(e.Flags) != 1 || e.Flags[0] != models.FlagRecent {
		t.Fatalf("default flags = %v, want [\\Recent]", e.Flags)
	}
}

// TestAppendDoesNotLeakInternalPointer pins the fix for the memory store
// returning internal pointers: callers previously held a *models.Email that
// aliased the store's own record, so a mutation outside the mutex was a data
// race. Now every getter returns a deep copy — mutating the returned value
// must not affect a subsequent read.
func TestAppendDoesNotLeakInternalPointer(t *testing.T) {
	s := New()
	u := newTestUser(t, s)
	mb := inboxOf(t, s, u.ID)

	stored := appendMsg(t, s, mb.ID, []string{models.FlagSeen})
	stored.Flags[0] = "MUTATED"
	stored.To = append(stored.To, "injected@evil.com")
	stored.Body = "tampered"

	got, err := s.List(context.Background(), mb.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	if hasFlag(got[0].Flags, "MUTATED") {
		t.Errorf("Flags mutation leaked: %v", got[0].Flags)
	}
	if hasStr(got[0].To, "injected@evil.com") {
		t.Errorf("To mutation leaked: %v", got[0].To)
	}
	if got[0].Body == "tampered" {
		t.Errorf("Body mutation leaked: %q", got[0].Body)
	}
}

// TestGetUserByEmailDoesNotLeakInternalPointer pins the same fix for the user
// getters — mutating the returned *models.User must not affect subsequent
// reads.
func TestGetUserByEmailDoesNotLeakInternalPointer(t *testing.T) {
	s := New()
	u := newTestUser(t, s)

	got, err := s.GetUserByEmail(context.Background(), u.Email)
	if err != nil || got == nil {
		t.Fatalf("GetUserByEmail: %v %v", err, got)
	}
	got.Email = "mutated@x.com"
	got.PasswordHash = "tampered"

	again, err := s.GetUserByEmail(context.Background(), "user@x.com")
	if err != nil {
		t.Fatalf("GetUserByEmail second call: %v", err)
	}
	if again == nil {
		t.Fatal("original user vanished after mutating returned copy — pointer leak")
	}
	if again.Email != "user@x.com" {
		t.Errorf("Email leaked mutation: %q", again.Email)
	}
	if again.PasswordHash != "hash" {
		t.Errorf("PasswordHash leaked mutation: %q", again.PasswordHash)
	}
}

func hasStr(slice []string, want string) bool {
	for _, s := range slice {
		if s == want {
			return true
		}
	}
	return false
}

// TestRoundTripPreservesCanonicalMessage is the ingress-to-fetch round trip:
// a message with full headers (Cc, Message-ID, In-Reply-To, display names)
// is parsed, stored via Append, and retrieved via List. Every field that
// message.Parse extracts must survive the storage round trip — this is the
// single test that pins the canonical representation contract.
func TestRoundTripPreservesCanonicalMessage(t *testing.T) {
	s := New()
	u := newTestUser(t, s)
	mb := inboxOf(t, s, u.ID)

	raw := []byte(strings.Join([]string{
		"From: Alice <alice@example.com>",
		"To: bob@example.com, Carol <carol@example.com>",
		"Cc: dave@example.com",
		"Subject: Round trip test",
		"Message-ID: <rt-123@example.com>",
		"In-Reply-To: <parent@example.com>",
		"Date: Mon, 02 Jan 2024 15:04:05 -0700",
		"",
		"This is the body.",
	}, "\r\n"))

	stored, err := s.Append(context.Background(), mb.ID, mailstore.Message{
		Parsed: message.Parse(raw),
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	// Verify the returned Email carries canonical data.
	if stored.Size != len(raw) {
		t.Errorf("returned Size = %d, want %d (len of raw)", stored.Size, len(raw))
	}
	if stored.MessageID != "<rt-123@example.com>" {
		t.Errorf("returned MessageID = %q", stored.MessageID)
	}
	if stored.InReplyTo != "<parent@example.com>" {
		t.Errorf("returned InReplyTo = %q", stored.InReplyTo)
	}
	if len(stored.Cc) != 1 || stored.Cc[0] != "dave@example.com" {
		t.Errorf("returned Cc = %v", stored.Cc)
	}
	if string(stored.Raw) != string(raw) {
		t.Errorf("returned Raw does not match input")
	}

	// Retrieve via List and verify the same data survives the read path.
	got, err := s.List(context.Background(), mb.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d emails, want 1", len(got))
	}
	e := got[0]

	if e.From != "Alice <alice@example.com>" {
		t.Errorf("From = %q", e.From)
	}
	if len(e.To) != 2 || e.To[0] != "bob@example.com" || e.To[1] != "Carol <carol@example.com>" {
		t.Errorf("To = %v", e.To)
	}
	if len(e.Cc) != 1 || e.Cc[0] != "dave@example.com" {
		t.Errorf("Cc = %v", e.Cc)
	}
	if e.Subject != "Round trip test" {
		t.Errorf("Subject = %q", e.Subject)
	}
	if e.MessageID != "<rt-123@example.com>" {
		t.Errorf("MessageID = %q", e.MessageID)
	}
	if e.InReplyTo != "<parent@example.com>" {
		t.Errorf("InReplyTo = %q", e.InReplyTo)
	}
	if e.Size != len(raw) {
		t.Errorf("Size = %d, want %d", e.Size, len(raw))
	}
	if string(e.Raw) != string(raw) {
		t.Errorf("Raw does not match input after List")
	}
	if !strings.Contains(e.Body, "This is the body") {
		t.Errorf("Body = %q", e.Body)
	}
}

// TestCopyPreservesRawAndMetadata verifies that Copy duplicates the raw bytes
// and canonical metadata, not just the flat display fields.
func TestCopyPreservesRawAndMetadata(t *testing.T) {
	s := New()
	u := newTestUser(t, s)
	inbox := inboxOf(t, s, u.ID)

	raw := []byte(strings.Join([]string{
		"From: sender@example.com",
		"To: user@x.com",
		"Cc: cc@example.com",
		"Subject: copy me",
		"Message-ID: <copy-123@example.com>",
		"",
		"body content",
	}, "\r\n"))

	src, err := s.Append(context.Background(), inbox.ID, mailstore.Message{
		Parsed: message.Parse(raw),
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	dest, err := s.CreateMailbox(context.Background(), u.ID, "Archive")
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}

	if err := s.Copy(context.Background(), inbox.ID, []int64{src.ID}, dest.ID); err != nil {
		t.Fatalf("copy: %v", err)
	}

	copied, err := s.List(context.Background(), dest.ID)
	if err != nil {
		t.Fatalf("list dest: %v", err)
	}
	if len(copied) != 1 {
		t.Fatalf("dest has %d, want 1", len(copied))
	}
	c := copied[0]

	if c.MessageID != "<copy-123@example.com>" {
		t.Errorf("copied MessageID = %q", c.MessageID)
	}
	if len(c.Cc) != 1 || c.Cc[0] != "cc@example.com" {
		t.Errorf("copied Cc = %v", c.Cc)
	}
	if c.Size != len(raw) {
		t.Errorf("copied Size = %d, want %d", c.Size, len(raw))
	}
	if string(c.Raw) != string(raw) {
		t.Errorf("copied Raw does not match source")
	}
}
