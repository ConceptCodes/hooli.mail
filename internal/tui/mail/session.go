// Package mail is the seam between the TUI's view layer and a mail server.
// The TUI model depends on Session, not on a concrete IMAP client, so the
// inbox/reading/sending behaviour can be faked in tests and the view code stays
// pure rendering.
//
// Sessions return data rather than mutating caller state: the previous design
// stashed a live IMAP client on the model and mutated model fields from inside
// command goroutines, which was both a data race and the reason the
// login/refresh fetch logic was duplicated.
package mail

import (
	"context"
	"time"
)

// Summary is a one-row inbox entry: enough to render a list row and the wax
// seal, no body.
type Summary struct {
	UID     uint32
	From    string
	Subject string
	Date    time.Time
	Seen    bool
}

// Full is a message opened for reading: header fields plus the body.
type Full struct {
	From    string
	To      []string
	Subject string
	Body    string
	Date    time.Time
}

// Outgoing is a message being sent.
type Outgoing struct {
	To      string
	Cc      string
	Bcc     string
	Subject string
	Body    string
}

// Session is the mail-server contract the TUI needs: list the inbox, open a
// message by UID, and send. Implementations (the IMAP adapter, a test fake)
// plug in behind this seam.
type Session interface {
	// Login connects to the server, authenticates, and returns the initial
	// inbox summary.
	Login(ctx context.Context, username, password string) ([]Summary, error)

	// Refresh reloads the inbox summary.
	Refresh(ctx context.Context) ([]Summary, error)

	// Fetch loads a full message by UID and marks it \Seen server-side.
	Fetch(ctx context.Context, uid uint32) (*Full, error)

	// Send transmits an outgoing message over SMTP submission.
	Send(ctx context.Context, out Outgoing) error

	// Logout closes the session.
	Logout(ctx context.Context) error
}
