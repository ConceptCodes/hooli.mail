package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

var migrations = []struct {
	name string
	sql  string
}{
	{
		name: "001_create_users",
		sql: `CREATE TABLE IF NOT EXISTS users (
			id BIGSERIAL PRIMARY KEY,
			email TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
	},
	{
		name: "002_create_mailboxes",
		sql: `CREATE TABLE IF NOT EXISTS mailboxes (
			id BIGSERIAL PRIMARY KEY,
			user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE(user_id, name)
		)`,
	},
	{
		name: "003_create_emails",
		sql: `CREATE TABLE IF NOT EXISTS emails (
			id BIGSERIAL PRIMARY KEY,
			mailbox_id BIGINT NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
			from_address TEXT NOT NULL,
			to_addresses TEXT[] NOT NULL,
			subject TEXT NOT NULL DEFAULT '',
			body TEXT NOT NULL DEFAULT '',
			flags TEXT[] NOT NULL DEFAULT '{"\\Recent"}',
			date TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			size INT NOT NULL DEFAULT 0
		)`,
	},
	{
		name: "004_create_email_indexes",
		sql:  `CREATE INDEX IF NOT EXISTS idx_emails_mailbox_id ON emails(mailbox_id)`,
	},
	{
		name: "005_create_mailbox_indexes",
		sql:  `CREATE INDEX IF NOT EXISTS idx_mailboxes_user_id ON mailboxes(user_id)`,
	},
	{
		name: "006_add_canonical_message_columns",
		sql: `ALTER TABLE emails
			ADD COLUMN IF NOT EXISTS raw_message BYTEA,
			ADD COLUMN IF NOT EXISTS cc_addresses TEXT[] NOT NULL DEFAULT '{}',
			ADD COLUMN IF NOT EXISTS message_id TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS in_reply_to TEXT NOT NULL DEFAULT ''`,
	},
}

func RunMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	for _, m := range migrations {
		if _, err := pool.Exec(ctx, m.sql); err != nil {
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
	}
	return nil
}
