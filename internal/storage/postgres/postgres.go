// Package postgres is the production mailstore.Store adapter, backed by
// PostgreSQL via pgx. The protocol servers never import this package directly
// for behaviour — they depend on mailstore.Store, and cmd/server wires this
// concrete adapter in.
package postgres

import (
	"context"
	"fmt"

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

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}

	s := &Store{pool: pool}
	if err := RunMigrations(ctx, pool); err != nil {
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return s, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) CreateUser(ctx context.Context, email, passwordHash string) (*models.User, error) {
	var u models.User
	err := s.pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash) VALUES ($1, $2)
		 RETURNING id, email, password_hash, created_at`,
		email, passwordHash,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}

	for _, mb := range models.DefaultMailboxes {
		if _, err := s.CreateMailbox(ctx, u.ID, mb); err != nil {
			return nil, fmt.Errorf("create mailbox %s: %w", mb, err)
		}
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
		if err == pgx.ErrNoRows {
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
	return mailboxes, nil
}

func (s *Store) GetMailboxByName(ctx context.Context, userID int64, name string) (*models.Mailbox, error) {
	var mb models.Mailbox
	err := s.pool.QueryRow(ctx,
		`SELECT id, user_id, name, created_at FROM mailboxes WHERE user_id = $1 AND name = $2`,
		userID, name,
	).Scan(&mb.ID, &mb.UserID, &mb.Name, &mb.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get mailbox: %w", err)
	}
	return &mb, nil
}

func (s *Store) DeleteMailbox(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM mailboxes WHERE id = $1`, id)
	return fmt.Errorf("delete mailbox: %w", err)
}

func (s *Store) RenameMailbox(ctx context.Context, id int64, newName string) error {
	_, err := s.pool.Exec(ctx, `UPDATE mailboxes SET name = $1 WHERE id = $2`, newName, id)
	return fmt.Errorf("rename mailbox: %w", err)
}

// Status computes all IMAP mailbox counters in one place. \Recent and unseen
// are derived from the actual flags columns rather than reported as "every
// message is recent and unseen", which was the previous behaviour.
func (s *Store) Status(ctx context.Context, mailboxID int64) (mailstore.Status, error) {
	var st mailstore.Status
	st.UIDValidity = 1

	queries := []struct {
		sql   string
		flag  string
		dst   *uint32
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
// delivery path; IMAP APPEND passes through the caller's flags.
func (s *Store) Append(ctx context.Context, mailboxID int64, msg mailstore.Message) (*models.Email, error) {
	flags := msg.Flags
	if len(flags) == 0 {
		flags = []string{models.FlagRecent}
	}
	size := len(msg.Body)

	var e models.Email
	err := s.pool.QueryRow(ctx,
		`INSERT INTO emails (mailbox_id, from_address, to_addresses, subject, body, flags, size)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, mailbox_id, from_address, to_addresses, subject, body, flags, date, size`,
		mailboxID, msg.From, msg.To, msg.Subject, msg.Body, flags, size,
	).Scan(&e.ID, &e.MailboxID, &e.From, &e.To, &e.Subject, &e.Body, &e.Flags, &e.Date, &e.Size)
	if err != nil {
		return nil, fmt.Errorf("append email: %w", err)
	}
	return &e, nil
}

// List returns every message in a Mailbox, newest first, with sequence numbers
// assigned in that order (1 = newest).
func (s *Store) List(ctx context.Context, mailboxID int64) ([]models.Email, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, mailbox_id, from_address, to_addresses, subject, body, flags, date, size
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
		if err := rows.Scan(&e.ID, &e.MailboxID, &e.From, &e.To, &e.Subject, &e.Body, &e.Flags, &e.Date, &e.Size); err != nil {
			return nil, fmt.Errorf("scan email: %w", err)
		}
		e.SeqNum = seqNum
		emails = append(emails, e)
	}
	return emails, nil
}

func (s *Store) SetFlags(ctx context.Context, emailID int64, flags []string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE emails SET flags = $1 WHERE id = $2`,
		flags, emailID,
	)
	return fmt.Errorf("set flags: %w", err)
}

func (s *Store) DeleteMessage(ctx context.Context, emailID int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM emails WHERE id = $1`, emailID)
	return fmt.Errorf("delete email: %w", err)
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
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx,
		`SELECT from_address, to_addresses, subject, body, flags
		 FROM emails WHERE mailbox_id = $1 AND id = ANY($2)`,
		srcMailboxID, ids,
	)
	if err != nil {
		return fmt.Errorf("copy select: %w", err)
	}
	defer rows.Close()

	type src struct {
		from, subject, body string
		to, flags            []string
	}
	var msgs []src
	for rows.Next() {
		var m src
		if err := rows.Scan(&m.from, &m.to, &m.subject, &m.body, &m.flags); err != nil {
			return fmt.Errorf("copy scan: %w", err)
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("copy rows: %w", err)
	}

	for _, m := range msgs {
		size := len(m.body)
		if _, err := tx.Exec(ctx,
			`INSERT INTO emails (mailbox_id, from_address, to_addresses, subject, body, flags, size)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			destMailboxID, m.from, m.to, m.subject, m.body, m.flags, size,
		); err != nil {
			return fmt.Errorf("copy insert: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("copy commit: %w", err)
	}
	return nil
}
