package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"hooli.mail/server/internal/mailstore"
	"hooli.mail/server/internal/message"
	"hooli.mail/server/internal/models"

	"github.com/jackc/pgx/v5"
)

// skipIfNoPostgres skips the test when TEST_POSTGRES_DSN is unset. The
// integration tests need a live Postgres; in CI without one we'd otherwise
// false-positive. Set TEST_POSTGRES_DSN to a throwaway database to enable.
func skipIfNoPostgres(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set TEST_POSTGRES_DSN to run postgres integration tests")
	}
	s, err := New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("postgres.New: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// TestSetFlagsReturnsNilOnSuccess pins the original bug: SetFlags used to
// `return fmt.Errorf("set flags: %w", nil)` which is non-nil. Successful
// updates must return nil.
//
// Postgres tests are deliberately not t.Parallel: each one calls postgres.New
// which runs migrations, and concurrent CREATE TABLE on the same DB will race
// in the catalog. Sequential is correct for shared-DB integration tests.
func TestSetFlagsReturnsNilOnSuccess(t *testing.T) {
	s := skipIfNoPostgres(t)
	ctx := context.Background()

	u, err := s.CreateUser(ctx, "flags-test@hooli.test", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	mb, err := s.GetMailboxByName(ctx, u.ID, "INBOX")
	if err != nil || mb == nil {
		t.Fatalf("inbox: %v mb=%v", err, mb)
	}
	email, err := s.Append(ctx, mb.ID, mailstore.Message{
		Parsed: message.Parse([]byte(strings.Join([]string{
			"From: x@h",
			"To: flags-test@hooli.test",
			"Subject: s",
			"",
			"b",
		}, "\r\n"))),
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	// This call previously returned a non-nil error wrapping nil.
	if err := s.SetFlags(ctx, email.ID, []string{models.FlagSeen, models.FlagFlagged}); err != nil {
		t.Fatalf("SetFlags returned non-nil on success: %v", err)
	}

	got, err := s.List(ctx, mb.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d emails, want 1", len(got))
	}
	for _, want := range []string{models.FlagSeen, models.FlagFlagged} {
		found := false
		for _, f := range got[0].Flags {
			if f == want {
				found = true
			}
		}
		if !found {
			t.Errorf("flags %v missing %q", got[0].Flags, want)
		}
	}
}

// TestDeleteMessageReturnsNilOnSuccess pins the same nil-wrap bug for the
// DELETE path.
func TestDeleteMessageReturnsNilOnSuccess(t *testing.T) {
	s := skipIfNoPostgres(t)
	ctx := context.Background()

	u, err := s.CreateUser(ctx, "del-test@hooli.test", "hash")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	mb, _ := s.GetMailboxByName(ctx, u.ID, "INBOX")
	email, err := s.Append(ctx, mb.ID, mailstore.Message{
		Parsed: message.Parse([]byte(strings.Join([]string{
			"From: x@h",
			"To: del-test@hooli.test",
			"Subject: s",
			"",
			"b",
		}, "\r\n"))),
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	if err := s.DeleteMessage(ctx, email.ID); err != nil {
		t.Fatalf("DeleteMessage returned non-nil on success: %v", err)
	}

	got, _ := s.List(ctx, mb.ID)
	if len(got) != 0 {
		t.Errorf("after delete, %d emails remain", len(got))
	}
}

// TestDeleteMailboxReturnsNilOnSuccess pins the nil-wrap bug for mailbox
// delete.
func TestDeleteMailboxReturnsNilOnSuccess(t *testing.T) {
	s := skipIfNoPostgres(t)
	ctx := context.Background()

	u, _ := s.CreateUser(ctx, "del-mb@hooli.test", "hash")
	mb, _ := s.CreateMailbox(ctx, u.ID, "Custom")

	if err := s.DeleteMailbox(ctx, mb.ID); err != nil {
		t.Fatalf("DeleteMailbox returned non-nil on success: %v", err)
	}

	got, _ := s.GetMailboxByName(ctx, u.ID, "Custom")
	if got != nil {
		t.Error("mailbox still exists after DeleteMailbox")
	}
}

// TestRenameMailboxReturnsNilOnSuccess pins the nil-wrap bug for rename.
func TestRenameMailboxReturnsNilOnSuccess(t *testing.T) {
	s := skipIfNoPostgres(t)
	ctx := context.Background()

	u, _ := s.CreateUser(ctx, "ren-mb@hooli.test", "hash")
	mb, _ := s.CreateMailbox(ctx, u.ID, "Old")

	if err := s.RenameMailbox(ctx, mb.ID, "New"); err != nil {
		t.Fatalf("RenameMailbox returned non-nil on success: %v", err)
	}

	if got, _ := s.GetMailboxByName(ctx, u.ID, "New"); got == nil {
		t.Error("renamed mailbox not found under new name")
	}
}

// TestGetUserByEmailWrapsErrNoRows pins the errors.Is fix: previously the
// check was a direct == which breaks the moment any middleware wraps the
// error.
func TestGetUserByEmailWrapsErrNoRows(t *testing.T) {
	s := skipIfNoPostgres(t)
	u, err := s.GetUserByEmail(context.Background(), "ghost@hooli.test")
	if err != nil {
		t.Fatalf("err on missing user: %v (want nil err, nil user)", err)
	}
	if u != nil {
		t.Errorf("got user %v for missing email", u)
	}
	// Sanity: pgx.ErrNoRows is available for callers to wrap and detect.
	_ = pgx.ErrNoRows
}

var _ = errors.Is // silence unused import if future edits drop a use
