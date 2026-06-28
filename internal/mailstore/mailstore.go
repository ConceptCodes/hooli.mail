package mailstore

import (
	"context"

	"hooli.mail/server/internal/models"
)

// Store is the seam between the protocol servers (SMTP, IMAP) and message
// persistence. A concrete implementation (the postgres adapter, an in-memory
// adapter for tests) is plugged in by the caller. The servers depend only on
// this interface, so storage can be swapped or faked without touching protocol
// code.
//
// Method granularity is intentionally domain-shaped rather than CRUD: bulk
// mailbox operations (Status, Expunge, Copy) live behind single calls so that
// mailbox semantics are owned by the Store, not reassembled by callers looping
// over fine-grained reads and writes.
type Store interface {
	// --- Users ---

	CreateUser(ctx context.Context, email, passwordHash string) (*models.User, error)
	GetUserByEmail(ctx context.Context, email string) (*models.User, error)

	// --- Mailboxes ---

	CreateMailbox(ctx context.Context, userID int64, name string) (*models.Mailbox, error)
	GetMailboxes(ctx context.Context, userID int64) ([]models.Mailbox, error)
	GetMailboxByName(ctx context.Context, userID int64, name string) (*models.Mailbox, error)
	DeleteMailbox(ctx context.Context, id int64) error
	RenameMailbox(ctx context.Context, id int64, newName string) error

	// Status returns the IMAP status counters for a Mailbox in one call:
	// total messages, \Recent count, unseen count, next UID and UID validity.
	Status(ctx context.Context, mailboxID int64) (Status, error)

	// --- Messages ---

	// Append stores msg in the given Mailbox. Empty Flags default to [\Recent].
	// The returned Email carries the assigned ID, Date and Size.
	Append(ctx context.Context, mailboxID int64, msg Message) (*models.Email, error)
	List(ctx context.Context, mailboxID int64) ([]models.Email, error)
	SetFlags(ctx context.Context, emailID int64, flags []string) error
	DeleteMessage(ctx context.Context, emailID int64) error

	// --- Deepened bulk operations ---

	// Expunge deletes every message in the Mailbox carrying \Deleted and returns
	// the number removed.
	Expunge(ctx context.Context, mailboxID int64) (int, error)

	// Copy duplicates the given messages into the destination Mailbox, preserving
	// their flags.
	Copy(ctx context.Context, srcMailboxID int64, ids []int64, destMailboxID int64) error
}

// Message is the "a sender wants to store this" value handed to Append. It is
// the parsed, domain form of a piece of mail before it becomes a stored Email.
type Message struct {
	From    string
	To      []string
	Subject string
	Body    string
	Flags   []string
}

// Status is the result of Store.Status — the IMAP mailbox counters computed in
// one place rather than pieced together by callers.
type Status struct {
	Messages    uint32
	Recent      uint32
	Unseen      uint32
	UIDNext     uint32
	UIDValidity uint32
}
