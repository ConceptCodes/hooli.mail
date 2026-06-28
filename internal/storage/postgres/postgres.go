package postgres

import (
	"context"
	"fmt"
	"time"

	"hooli.mail/server/internal/models"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

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

func (s *Store) GetUserByID(ctx context.Context, id int64) (*models.User, error) {
	var u models.User
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, password_hash, created_at FROM users WHERE id = $1`,
		id,
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

func (s *Store) GetMailboxByID(ctx context.Context, id int64) (*models.Mailbox, error) {
	var mb models.Mailbox
	err := s.pool.QueryRow(ctx,
		`SELECT id, user_id, name, created_at FROM mailboxes WHERE id = $1`,
		id,
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

func (s *Store) CreateEmail(ctx context.Context, mailboxID int64, from string, to []string, subject, body string) (*models.Email, error) {
	flags := []string{models.FlagRecent}
	size := len(body)

	var e models.Email
	err := s.pool.QueryRow(ctx,
		`INSERT INTO emails (mailbox_id, from_address, to_addresses, subject, body, flags, size)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, mailbox_id, from_address, to_addresses, subject, body, flags, date, size`,
		mailboxID, from, to, subject, body, flags, size,
	).Scan(&e.ID, &e.MailboxID, &e.From, &e.To, &e.Subject, &e.Body, &e.Flags, &e.Date, &e.Size)
	if err != nil {
		return nil, fmt.Errorf("create email: %w", err)
	}
	return &e, nil
}

func (s *Store) GetEmail(ctx context.Context, id int64) (*models.Email, error) {
	var e models.Email
	err := s.pool.QueryRow(ctx,
		`SELECT id, mailbox_id, from_address, to_addresses, subject, body, flags, date, size
		 FROM emails WHERE id = $1`,
		id,
	).Scan(&e.ID, &e.MailboxID, &e.From, &e.To, &e.Subject, &e.Body, &e.Flags, &e.Date, &e.Size)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get email: %w", err)
	}
	return &e, nil
}

func (s *Store) GetEmails(ctx context.Context, mailboxID int64) ([]models.Email, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, mailbox_id, from_address, to_addresses, subject, body, flags, date, size
		 FROM emails WHERE mailbox_id = $1 ORDER BY date DESC`,
		mailboxID,
	)
	if err != nil {
		return nil, fmt.Errorf("get emails: %w", err)
	}
	defer rows.Close()

	var emails []models.Email
	seqNum := uint32(len(emails))
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

func (s *Store) GetEmailCount(ctx context.Context, mailboxID int64) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM emails WHERE mailbox_id = $1`,
		mailboxID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("get email count: %w", err)
	}
	return count, nil
}

func (s *Store) UpdateFlags(ctx context.Context, emailID int64, flags []string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE emails SET flags = $1 WHERE id = $2`,
		flags, emailID,
	)
	return fmt.Errorf("update flags: %w", err)
}

func (s *Store) DeleteEmail(ctx context.Context, emailID int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM emails WHERE id = $1`, emailID)
	return fmt.Errorf("delete email: %w", err)
}

func (s *Store) DeleteEmails(ctx context.Context, mailboxID int64, emailIDs []int64) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM emails WHERE mailbox_id = $1 AND id = ANY($2)`,
		mailboxID, emailIDs,
	)
	return fmt.Errorf("delete emails: %w", err)
}

func (s *Store) MoveEmail(ctx context.Context, emailID int64, destMailboxID int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE emails SET mailbox_id = $1 WHERE id = $2`,
		destMailboxID, emailID,
	)
	return fmt.Errorf("move email: %w", err)
}

func (s *Store) GetLastUID(ctx context.Context, mailboxID int64) (uint32, error) {
	var lastID int64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(id), 0) FROM emails WHERE mailbox_id = $1`,
		mailboxID,
	).Scan(&lastID)
	if err != nil {
		return 0, fmt.Errorf("get last uid: %w", err)
	}
	return uint32(lastID), nil
}

func (s *Store) GetEmailsByUIDs(ctx context.Context, mailboxID int64, uids []uint32) ([]models.Email, error) {
	ids := make([]int64, len(uids))
	for i, uid := range uids {
		ids[i] = int64(uid)
	}

	rows, err := s.pool.Query(ctx,
		`SELECT id, mailbox_id, from_address, to_addresses, subject, body, flags, date, size
		 FROM emails WHERE mailbox_id = $1 AND id = ANY($2) ORDER BY id`,
		mailboxID, ids,
	)
	if err != nil {
		return nil, fmt.Errorf("get emails by uids: %w", err)
	}
	defer rows.Close()

	var emails []models.Email
	for rows.Next() {
		var e models.Email
		if err := rows.Scan(&e.ID, &e.MailboxID, &e.From, &e.To, &e.Subject, &e.Body, &e.Flags, &e.Date, &e.Size); err != nil {
			return nil, fmt.Errorf("scan email: %w", err)
		}
		emails = append(emails, e)
	}
	return emails, nil
}

func (s *Store) CreateSession(_ context.Context, _ int64) (string, time.Time, error) {
	return "", time.Time{}, fmt.Errorf("not implemented")
}

func (s *Store) ValidateSession(_ context.Context, _ string) (*models.User, error) {
	return nil, fmt.Errorf("not implemented")
}
