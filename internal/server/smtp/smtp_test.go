package smtp

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"hooli.mail/server/internal/auth"
	"hooli.mail/server/internal/mailstore"
	"hooli.mail/server/internal/models"
	"hooli.mail/server/internal/storage/memory"

	gosmtp "github.com/emersion/go-smtp"
)

// newBackend wires an SMTP backend against a fresh memory store with one
// pre-hashed user (alice@hooli.test). Returns the backend and the password
// ("wonderland") so tests can drive AuthPlain.
func newBackend(t *testing.T, requireAuth bool) (*Backend, *memory.Store, *auth.Authenticator) {
	t.Helper()
	store := memory.New()
	authn := auth.NewAuthenticator(store)
	return NewBackend(store, authn, requireAuth, context.Background()), store, authn
}

func mustCreateUser(t *testing.T, store *memory.Store, email string) *models.User {
	t.Helper()
	u, err := store.CreateUser(context.Background(), email, "hash")
	if err != nil {
		t.Fatalf("create user %s: %v", email, err)
	}
	return u
}

// TestEnforceSender pins the fix for the strings.Contains hole in MAIL FROM:
// an authenticated user could previously claim ba@x.com or a@x.com.evil.com
// when their own address was a@x.com. Now we parse and compare.
func TestEnforceSender(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		from       string
		authorised string
		want       string
	}{
		{"exact match", "<alice@hooli.test>", "alice@hooli.test", "alice@hooli.test"},
		{"exact without brackets", "alice@hooli.test", "alice@hooli.test", "alice@hooli.test"},
		{"prefix attack", "ba@hooli.test", "alice@hooli.test", "alice@hooli.test"},
		{"suffix attack", "alice@hooli.test.evil.com", "alice@hooli.test", "alice@hooli.test"},
		{"garbage", "not an address", "alice@hooli.test", "alice@hooli.test"},
		{"display name form", "Alice <alice@hooli.test>", "alice@hooli.test", "alice@hooli.test"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := enforceSender(c.from, c.authorised)
			if got != c.want {
				t.Errorf("enforceSender(%q, %q) = %q, want %q", c.from, c.authorised, got, c.want)
			}
		})
	}
}

// TestSessionRequiresAuthOnSubmission ensures that a backend built with
// requireAuth=true refuses MAIL FROM before AUTH PLAIN, with the proper 530
// SMTP error code so the client knows to authenticate.
func TestSessionRequiresAuthOnSubmission(t *testing.T) {
	t.Parallel()
	b, _, _ := newBackend(t, true)

	session := &Session{backend: b, ctx: context.Background()}
	err := session.Mail("alice@hooli.test", &gosmtp.MailOptions{})
	if err == nil {
		t.Fatal("Mail succeeded without auth on requireAuth=true; want 530")
	}
	smtpErr, ok := err.(*gosmtp.SMTPError)
	if !ok {
		t.Fatalf("err type = %T, want *gosmtp.SMTPError", err)
	}
	if smtpErr.Code != 530 {
		t.Errorf("SMTP code = %d, want 530", smtpErr.Code)
	}
}

// TestSessionUnauth AllowsAnonymousDelivery checks that the MTA-receiving
// path (requireAuth=false) still accepts anonymous MAIL FROM so external mail
// can flow in. This is not the open-relay path — the recipient must be a
// real local user, which is exercised in TestSessionDeliversToRecipient.
func TestSessionUnauthAllowsAnonymousDelivery(t *testing.T) {
	t.Parallel()
	b, _, _ := newBackend(t, false)

	session := &Session{backend: b, ctx: context.Background()}
	if err := session.Mail("stranger@external.com", &gosmtp.MailOptions{}); err != nil {
		t.Fatalf("Mail on requireAuth=false failed: %v", err)
	}
	if session.from != "stranger@external.com" {
		t.Errorf("session.from = %q, want stranger@external.com", session.from)
	}
}

// TestSessionRcptRejectsBadAddress pins the new 501 path on RCPT TO. The old
// code silently stripped angle brackets and accepted any string.
func TestSessionRcptRejectsBadAddress(t *testing.T) {
	t.Parallel()
	b, _, _ := newBackend(t, false)
	session := &Session{backend: b, ctx: context.Background()}

	err := session.Rcpt("not an address", &gosmtp.RcptOptions{})
	if err == nil {
		t.Fatal("Rcpt accepted garbage; want 501")
	}
	smtpErr, ok := err.(*gosmtp.SMTPError)
	if !ok {
		t.Fatalf("err type = %T, want *gosmtp.SMTPError", err)
	}
	if smtpErr.Code != 501 {
		t.Errorf("SMTP code = %d, want 501", smtpErr.Code)
	}
}

// TestSessionDeliversToRecipient is the end-to-end DATA path: anonymous SMTP
// to a known local user must land in that user's INBOX.
func TestSessionDeliversToRecipient(t *testing.T) {
	t.Parallel()
	b, store, _ := newBackend(t, false)
	mustCreateUser(t, store, "alice@hooli.test")

	session := &Session{backend: b, ctx: context.Background()}
	if err := session.Mail("stranger@external.com", &gosmtp.MailOptions{}); err != nil {
		t.Fatalf("Mail: %v", err)
	}
	if err := session.Rcpt("<alice@hooli.test>", &gosmtp.RcptOptions{}); err != nil {
		t.Fatalf("Rcpt: %v", err)
	}

	raw := []byte("From: stranger@external.com\r\nTo: alice@hooli.test\r\nSubject: hi\r\n\r\nhello\r\n")
	if err := session.Data(bytes.NewReader(raw)); err != nil {
		t.Fatalf("Data: %v", err)
	}

	mb, err := store.GetMailboxByName(context.Background(), 1, "INBOX")
	if err != nil || mb == nil {
		t.Fatalf("inbox: %v mb=%v", err, mb)
	}
	emails, err := store.List(context.Background(), mb.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(emails) != 1 {
		t.Fatalf("inbox has %d emails, want 1", len(emails))
	}
	if !strings.Contains(emails[0].Body, "hello") {
		t.Errorf("body = %q, want it to contain 'hello'", emails[0].Body)
	}
}

// TestSessionDataAllRecipientsFailReturnError pins the fix for the silent-
// swallow: previously Data returned nil even when every delivery failed,
// telling the SMTP client 250 OK. Now it surfaces the last error.
func TestSessionDataAllRecipientsFailReturnError(t *testing.T) {
	t.Parallel()
	b, _, _ := newBackend(t, false)
	// No users created — every RCPT will fail lookup.

	session := &Session{backend: b, ctx: context.Background()}
	if err := session.Mail("stranger@external.com", &gosmtp.MailOptions{}); err != nil {
		t.Fatalf("Mail: %v", err)
	}
	if err := session.Rcpt("<ghost@hooli.test>", &gosmtp.RcptOptions{}); err != nil {
		t.Fatalf("Rcpt: %v", err)
	}

	err := session.Data(bytes.NewReader([]byte("Subject: x\r\n\r\nx\r\n")))
	if err == nil {
		t.Fatal("Data returned nil when all deliveries failed; want an error")
	}
}

// TestSessionResetClearsUser pins the hygiene fix: RSET used to leave
// s.user intact after the connection's RSET, so a subsequent MAIL FROM
// would still be authorised.
func TestSessionResetClearsUser(t *testing.T) {
	t.Parallel()
	b, store, _ := newBackend(t, true)
	user := mustCreateUser(t, store, "alice@hooli.test")

	hash, err := auth.HashPassword("wonderland")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	user.PasswordHash = hash

	session := &Session{backend: b, ctx: context.Background()}
	if err := session.AuthPlain("alice@hooli.test", "wonderland"); err != nil {
		t.Fatalf("AuthPlain: %v", err)
	}
	if session.user == nil {
		t.Fatal("user nil after successful auth")
	}
	session.Reset()
	if session.user != nil {
		t.Error("user still set after Reset; want nil (RSET must clear auth)")
	}
	if session.from != "" || len(session.to) != 0 {
		t.Errorf("Reset left from=%q to=%v; want cleared", session.from, session.to)
	}
}

// TestSessionAuthPlainWrongPassword confirms that the backend delegates to
// the authenticator and surfaces ErrInvalidCredentials.
func TestSessionAuthPlainWrongPassword(t *testing.T) {
	t.Parallel()
	b, store, _ := newBackend(t, true)
	user := mustCreateUser(t, store, "alice@hooli.test")
	hash, err := auth.HashPassword("wonderland")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	user.PasswordHash = hash

	session := &Session{backend: b, ctx: context.Background()}
	if err := session.AuthPlain("alice@hooli.test", "wrong"); err == nil {
		t.Fatal("AuthPlain with wrong password succeeded; want error")
	}
	if session.user != nil {
		t.Error("user set after failed auth")
	}
}

// TestSessionAppendReplacesMailboxTests is a coverage helper that the IMAP
// adapter test file can reuse. We assert the mailstore.Store contract holds
// here too so a backend regression is caught without a running server.
func TestBackendSatisfiesMailstore(t *testing.T) {
	t.Parallel()
	b, _, _ := newBackend(t, false)
	var _ mailstore.Store = b.store
}
