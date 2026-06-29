// Package postgres is the production mailstore.Store adapter, backed by
// PostgreSQL via pgx. The protocol servers never import this package directly
// for behaviour — they depend on mailstore.Store, and cmd/server wires this
// concrete adapter in.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"hooli.mail/server/internal/mailbox"
	"hooli.mail/server/internal/mailstore"
	"hooli.mail/server/internal/models"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

var _ mailstore.Store = (*Store)(nil)

func New(ctx context.Context, connString string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	// If Ping or RunMigrations fail, close the pool so we don't leak
	// connections. Previously a failing Ping left the pool open because
	// store.Close() was never wired up.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	s := &Store{pool: pool}
	if err := RunMigrations(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return s, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

// CreateUser inserts the user and seeds the DefaultMailboxes inside a single
// transaction. If any mailbox insert fails the whole transaction rolls back,
// so we never persist a half-created account (e.g. an INBOX-less user that
// every later delivery would crash on).
func (s *Store) CreateUser(ctx context.Context, email, passwordHash string) (*models.User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("create user begin: %w", err)
	}
	// pgx's deferred Rollback returns ErrTxClosed after a successful Commit;
	// that's expected and not an error we want to surface. We still need the
	// defer to run on every return path that doesn't reach Commit.
	defer func() { _ = tx.Rollback(ctx) }()

	var u models.User
	err = tx.QueryRow(ctx,
		`INSERT INTO users (email, password_hash) VALUES ($1, $2)
		 RETURNING id, email, password_hash, created_at`,
		email, passwordHash,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}

	for _, mb := range models.DefaultMailboxes {
		if _, err := tx.Exec(ctx,
			`INSERT INTO mailboxes (user_id, name) VALUES ($1, $2)`,
			u.ID, mb,
		); err != nil {
			return nil, fmt.Errorf("create mailbox %s: %w", mb, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("create user commit: %w", err)
	}

	return &u, nil
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (*models.User, error) {
	var u models.User
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, password_hash, created_at FROM users WHERE email = $1`,
		email,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get user: %w", err)
	}
	return &u, nil
}

func (s *Store) CreateMailbox(ctx context.Context, userID int64, name string) (*models.Mailbox, error) {
	var mb models.Mailbox
	err := s.pool.QueryRow(ctx,
		`INSERT INTO mailboxes (user_id, name) VALUES ($1, $2)
		 RETURNING id, user_id, name, created_at`,
		userID, name,
	).Scan(&mb.ID, &mb.UserID, &mb.Name, &mb.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create mailbox: %w", err)
	}
	return &mb, nil
}

func (s *Store) GetMailboxes(ctx context.Context, userID int64) ([]models.Mailbox, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, user_id, name, created_at FROM mailboxes WHERE user_id = $1 ORDER BY name`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("get mailboxes: %w", err)
	}
	defer rows.Close()

	var mailboxes []models.Mailbox
	for rows.Next() {
		var mb models.Mailbox
		if err := rows.Scan(&mb.ID, &mb.UserID, &mb.Name, &mb.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan mailbox: %w", err)
		}
		mailboxes = append(mailboxes, mb)
	}
	// rows.Err surfaces the error that ended the iterator (network blip,
	// context cancellation). Without it a truncated result set looks like a
	// successful shorter one.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mailboxes: %w", err)
	}
	return mailboxes, nil
}

func (s *Store) GetMailboxByName(ctx context.Context, userID int64, name string) (*models.Mailbox, error) {
	var mb models.Mailbox
	err := s.pool.QueryRow(ctx,
		`SELECT id, user_id, name, created_at FROM mailboxes WHERE user_id = $1 AND name = $2`,
		userID, name,
	).Scan(&mb.ID, &mb.UserID, &mb.Name, &mb.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get mailbox: %w", err)
	}
	return &mb, nil
}

func (s *Store) DeleteMailbox(ctx context.Context, id int64) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM mailboxes WHERE id = $1`, id); err != nil {
		return fmt.Errorf("delete mailbox: %w", err)
	}
	return nil
}

func (s *Store) RenameMailbox(ctx context.Context, id int64, newName string) error {
	if _, err := s.pool.Exec(ctx, `UPDATE mailboxes SET name = $1 WHERE id = $2`, newName, id); err != nil {
		return fmt.Errorf("rename mailbox: %w", err)
	}
	return nil
}

// Status computes all IMAP mailbox counters in one place. \Recent and unseen
// are derived from the actual flags columns rather than reported as "every
// message is recent and unseen", which was the previous behaviour.
func (s *Store) Status(ctx context.Context, mailboxID int64) (mailstore.Status, error) {
	var st mailstore.Status
	st.UIDValidity = 1

	queries := []struct {
		sql  string
		flag string
		dst  *uint32
	}{
		{`SELECT COUNT(*) FROM emails WHERE mailbox_id = $1`, "", &st.Messages},
		{`SELECT COUNT(*) FROM emails WHERE mailbox_id = $1 AND $2 = ANY(flags)`, models.FlagRecent, &st.Recent},
		{`SELECT COUNT(*) FROM emails WHERE mailbox_id = $1 AND NOT ($2 = ANY(flags))`, models.FlagSeen, &st.Unseen},
	}

	for _, q := range queries {
		var n int
		var err error
		if q.flag == "" {
			err = s.pool.QueryRow(ctx, q.sql, mailboxID).Scan(&n)
		} else {
			err = s.pool.QueryRow(ctx, q.sql, mailboxID, q.flag).Scan(&n)
		}
		if err != nil {
			return mailstore.Status{}, fmt.Errorf("status count: %w", err)
		}
		*q.dst = uint32(n)
	}

	var lastID int64
	if err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(id), 0) FROM emails WHERE mailbox_id = $1`,
		mailboxID,
	).Scan(&lastID); err != nil {
		return mailstore.Status{}, fmt.Errorf("status uidnext: %w", err)
	}
	st.UIDNext = uint32(lastID) + 1

	return st, nil
}

// Append stores a Message. Empty Flags default to [\Recent], matching the
// delivery path; IMAP APPEND passes through the caller's flags. Raw message
// bytes are stored alongside derived metadata so IMAP BODY[] and RFC822.SIZE
// are faithful to what was received. Size is len(Raw), not len(Body).
func (s *Store) Append(ctx context.Context, mailboxID int64, msg mailstore.Message) (*models.Email, error) {
	flags := msg.Flags
	if len(flags) == 0 {
		flags = []string{models.FlagRecent}
	}
	p := msg.Parsed
	size := p.Size

	var e models.Email
	err := s.pool.QueryRow(ctx,
		`INSERT INTO emails (mailbox_id, from_address, to_addresses, cc_addresses, subject, body, raw_message, message_id, in_reply_to, flags, size)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 RETURNING id, mailbox_id, from_address, to_addresses, cc_addresses, subject, body, raw_message, message_id, in_reply_to, flags, date, size`,
		mailboxID, p.From, p.To, p.Cc, p.Subject, p.Body, p.Raw, p.MessageID, p.InReplyTo, flags, size,
	).Scan(&e.ID, &e.MailboxID, &e.From, &e.To, &e.Cc, &e.Subject, &e.Body, &e.Raw, &e.MessageID, &e.InReplyTo, &e.Flags, &e.Date, &e.Size)
	if err != nil {
		return nil, fmt.Errorf("append email: %w", err)
	}
	return &e, nil
}

// List returns every message in a Mailbox, newest first, with sequence numbers
// assigned in that order (1 = newest).
func (s *Store) List(ctx context.Context, mailboxID int64) ([]models.Email, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, mailbox_id, from_address, to_addresses, cc_addresses, subject, body, raw_message, message_id, in_reply_to, flags, date, size
		 FROM emails WHERE mailbox_id = $1 ORDER BY date DESC`,
		mailboxID,
	)
	if err != nil {
		return nil, fmt.Errorf("list emails: %w", err)
	}
	defer rows.Close()

	var emails []models.Email
	seqNum := uint32(0)
	for rows.Next() {
		seqNum++
		var e models.Email
		if err := rows.Scan(&e.ID, &e.MailboxID, &e.From, &e.To, &e.Cc, &e.Subject, &e.Body, &e.Raw, &e.MessageID, &e.InReplyTo, &e.Flags, &e.Date, &e.Size); err != nil {
			return nil, fmt.Errorf("scan email: %w", err)
		}
		e.SeqNum = seqNum
		emails = append(emails, e)
	}
	// rows.Err catches the iterator's terminal error (ctx cancellation,
	// connection loss). Without it a partial list looks like a successful
	// short result.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate emails: %w", err)
	}
	return emails, nil
}

func (s *Store) SetFlags(ctx context.Context, emailID int64, flags []string) error {
	if _, err := s.pool.Exec(ctx,
		`UPDATE emails SET flags = $1 WHERE id = $2`,
		flags, emailID,
	); err != nil {
		return fmt.Errorf("set flags: %w", err)
	}
	return nil
}

// Search evaluates the criteria in Go using mailbox.Match. This keeps the
// semantic logic in one place (the mailbox package) rather than splitting it
// between SQL and Go. The query loads only the columns Match needs.
func (s *Store) Search(ctx context.Context, mailboxID int64, criteria mailbox.SearchCriteria) ([]int64, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, from_address, to_addresses, cc_addresses, subject, body, flags, date, size
		 FROM emails WHERE mailbox_id = $1`,
		mailboxID,
	)
	if err != nil {
		return nil, fmt.Errorf("search emails: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var e models.Email
		e.MailboxID = mailboxID
		if err := rows.Scan(&e.ID, &e.From, &e.To, &e.Cc, &e.Subject, &e.Body, &e.Flags, &e.Date, &e.Size); err != nil {
			return nil, fmt.Errorf("search scan: %w", err)
		}
		if mailbox.Match(e, criteria) {
			ids = append(ids, e.ID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search rows: %w", err)
	}
	return ids, nil
}

// UpdateFlags applies a flag operation to multiple messages in one transaction.
// Each message's current flags are read, mailbox.ApplyFlags computes the new
// set, and the result is written back. The transaction ensures that a partial
// failure leaves no message half-updated.
func (s *Store) UpdateFlags(ctx context.Context, mailboxID int64, ids []int64, op mailbox.FlagOperation, flags []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("update flags begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx,
		`SELECT id, flags FROM emails WHERE mailbox_id = $1 AND id = ANY($2)`,
		mailboxID, ids,
	)
	if err != nil {
		return fmt.Errorf("update flags select: %w", err)
	}
	defer rows.Close()

	type entry struct {
		id       int64
		oldFlags []string
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.oldFlags); err != nil {
			return fmt.Errorf("update flags scan: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("update flags rows: %w", err)
	}

	for _, e := range entries {
		newFlags := mailbox.ApplyFlags(e.oldFlags, op, flags)
		if _, err := tx.Exec(ctx,
			`UPDATE emails SET flags = $1 WHERE id = $2`,
			newFlags, e.id,
		); err != nil {
			return fmt.Errorf("update flags write: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("update flags commit: %w", err)
	}
	return nil
}

func (s *Store) DeleteMessage(ctx context.Context, emailID int64) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM emails WHERE id = $1`, emailID); err != nil {
		return fmt.Errorf("delete email: %w", err)
	}
	return nil
}

// Expunge removes every message in the Mailbox carrying \Deleted in a single
// statement, returning the number removed.
func (s *Store) Expunge(ctx context.Context, mailboxID int64) (int, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM emails WHERE mailbox_id = $1 AND $2 = ANY(flags)`,
		mailboxID, models.FlagDeleted,
	)
	if err != nil {
		return 0, fmt.Errorf("expunge: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// Copy duplicates the selected messages into the destination Mailbox inside one
// transaction, preserving flags (which the previous loop-based Append dropped).
func (s *Store) Copy(ctx context.Context, srcMailboxID int64, ids []int64, destMailboxID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("copy begin: %w", err)
	}
	// See CreateUser for why Rollback is deferred inside a closure.
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx,
		`SELECT from_address, to_addresses, cc_addresses, subject, body, raw_message, message_id, in_reply_to, flags, size
		 FROM emails WHERE mailbox_id = $1 AND id = ANY($2)`,
		srcMailboxID, ids,
	)
	if err != nil {
		return fmt.Errorf("copy select: %w", err)
	}
	defer rows.Close()

	type src struct {
		from, subject, body, messageID, inReplyTo string
		to, cc, flags                             []string
		raw                                       []byte
		size                                      int
	}
	var msgs []src
	for rows.Next() {
		var m src
		if err := rows.Scan(&m.from, &m.to, &m.cc, &m.subject, &m.body, &m.raw, &m.messageID, &m.inReplyTo, &m.flags, &m.size); err != nil {
			return fmt.Errorf("copy scan: %w", err)
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("copy rows: %w", err)
	}

	for _, m := range msgs {
		if _, err := tx.Exec(ctx,
			`INSERT INTO emails (mailbox_id, from_address, to_addresses, cc_addresses, subject, body, raw_message, message_id, in_reply_to, flags, size)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			destMailboxID, m.from, m.to, m.cc, m.subject, m.body, m.raw, m.messageID, m.inReplyTo, m.flags, m.size,
		); err != nil {
			return fmt.Errorf("copy insert: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("copy commit: %w", err)
	}
	return nil
}
