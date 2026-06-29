package imap

import (
	"context"
	"sort"
	"testing"
	"time"

	"hooli.mail/server/internal/mailstore"
	"hooli.mail/server/internal/models"
	"hooli.mail/server/internal/storage/memory"

	imap "github.com/emersion/go-imap"
)

// flagsEqual sorts then compares two flag slices so tests are order-
// insensitive — applyFlagsOp returns from a map iteration.
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

// TestApplyFlagsOpSetAll previously pinned the bug where SetFlags reset the
// flag set inside the loop and only the final flag survived. With multiple
// flags passed to STORE FLAGS, all must be present in the result.
func TestApplyFlagsOpSetAll(t *testing.T) {
	t.Parallel()
	current := []string{models.FlagSeen, models.FlagRecent}
	got := applyFlagsOp(current, imap.SetFlags, []string{models.FlagSeen, models.FlagFlagged})
	if !flagsEqual(got, models.FlagSeen, models.FlagFlagged) {
		t.Errorf("SetFlags = %v, want [\\Seen \\Flagged] (current must be replaced, not collapsed)", got)
	}
}

func TestApplyFlagsOpAddPreservesExisting(t *testing.T) {
	t.Parallel()
	current := []string{models.FlagSeen}
	got := applyFlagsOp(current, imap.AddFlags, []string{models.FlagFlagged, models.FlagAnswered})
	if !flagsEqual(got, models.FlagSeen, models.FlagFlagged, models.FlagAnswered) {
		t.Errorf("AddFlags = %v, want [\\Seen \\Flagged \\Answered]", got)
	}
}

func TestApplyFlagsOpRemove(t *testing.T) {
	t.Parallel()
	current := []string{models.FlagSeen, models.FlagFlagged, models.FlagRecent}
	got := applyFlagsOp(current, imap.RemoveFlags, []string{models.FlagSeen, models.FlagRecent})
	if !flagsEqual(got, models.FlagFlagged) {
		t.Errorf("RemoveFlags = %v, want [\\Flagged]", got)
	}
}

func TestApplyFlagsOpSetEmptyClearsAll(t *testing.T) {
	t.Parallel()
	current := []string{models.FlagSeen, models.FlagFlagged}
	got := applyFlagsOp(current, imap.SetFlags, nil)
	if len(got) != 0 {
		t.Errorf("SetFlags empty = %v, want []", got)
	}
}

// --- Search criteria helpers ---

func TestMatchFlags(t *testing.T) {
	t.Parallel()
	flags := []string{models.FlagSeen, models.FlagFlagged}
	if !matchFlags(flags, []string{models.FlagSeen}, nil) {
		t.Error("expected match when WithFlags=[Seen] and Seen is present")
	}
	if matchFlags(flags, []string{models.FlagSeen, models.FlagDeleted}, nil) {
		t.Error("expected no match when WithFlags contains absent Deleted")
	}
	if matchFlags(flags, nil, []string{models.FlagSeen}) {
		t.Error("expected no match when WithoutFlags=[Seen] and Seen is present")
	}
	if !matchFlags(flags, nil, []string{models.FlagDeleted}) {
		t.Error("expected match when WithoutFlags=[Deleted] and Deleted is absent")
	}
}

func TestMatchFlagsCaseInsensitive(t *testing.T) {
	t.Parallel()
	flags := []string{"\\Seen"}
	if !matchFlags(flags, []string{"\\SEEN"}, nil) {
		t.Error("flag matching must be case-insensitive")
	}
}

func TestMatchDateRange(t *testing.T) {
	t.Parallel()
	mid := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	since := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC)
	if !matchDateRange(mid, since, time.Time{}) {
		t.Error("expected match: mid is after Since")
	}
	if matchDateRange(since.Add(-24*time.Hour), since, time.Time{}) {
		t.Error("expected no match: date is before Since")
	}
	if !matchDateRange(mid, time.Time{}, before) {
		t.Error("expected match: mid is before Before")
	}
	if matchDateRange(before, time.Time{}, before) {
		t.Error("expected no match: date equals Before (Before is exclusive)")
	}
}

func TestMatchSize(t *testing.T) {
	t.Parallel()
	if matchSize(100, 100, 0) {
		t.Error("Larger is exclusive: 100 not larger than 100")
	}
	if !matchSize(101, 100, 0) {
		t.Error("expected match: 101 > 100")
	}
	if matchSize(100, 0, 100) {
		t.Error("Smaller is exclusive: 100 not smaller than 100")
	}
	if !matchSize(99, 0, 100) {
		t.Error("expected match: 99 < 100")
	}
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
	return NewBackend(store, nil, context.Background()), u
}

func inboxOf(t *testing.T, b *IMAPBackend, u *models.User) *models.Mailbox {
	t.Helper()
	mb, err := b.store.GetMailboxByName(context.Background(), u.ID, "INBOX")
	if err != nil || mb == nil {
		t.Fatalf("inbox lookup: %v mb=%v", err, mb)
	}
	return mb
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
		From: "boss@hooli.test", To: []string{"alice@hooli.test"},
		Subject: "promo", Body: "you got it",
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
		From: "a@h", To: []string{"alice@hooli.test"}, Subject: "1", Body: "1",
	}); err != nil {
		t.Fatalf("append 1: %v", err)
	}
	seen, err := b.store.Append(context.Background(), mb.ID, mailstore.Message{
		From: "b@h", To: []string{"alice@hooli.test"}, Subject: "2", Body: "2",
		Flags: []string{models.FlagSeen},
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
		From: "alice-mentor@external.com", To: []string{"alice@hooli.test"},
		Subject: "Quarterly review", Body: "schedule for Q3",
	}); err != nil {
		t.Fatalf("append 1: %v", err)
	}
	if _, err := b.store.Append(context.Background(), mb.ID, mailstore.Message{
		From: "noreply@spam.com", To: []string{"alice@hooli.test"},
		Subject: "WINNER", Body: "click here",
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
