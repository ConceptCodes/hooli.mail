package models

import "time"

type User struct {
	ID           int64     `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

type Mailbox struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type Email struct {
	ID        int64     `json:"id"`
	MailboxID int64     `json:"mailbox_id"`
	From      string    `json:"from"`
	To        []string  `json:"to"`
	Subject   string    `json:"subject"`
	Body      string    `json:"body"`
	Flags     []string  `json:"flags"`
	Date      time.Time `json:"date"`
	Size      int       `json:"size"`
	SeqNum    uint32    `json:"seq_num"`
}

const (
	FlagRecent   = "\\Recent"
	FlagSeen     = "\\Seen"
	FlagDeleted  = "\\Deleted"
	FlagFlagged  = "\\Flagged"
	FlagDraft    = "\\Draft"
	FlagAnswered = "\\Answered"
)

var DefaultMailboxes = []string{"INBOX", "Sent", "Drafts", "Trash", "Junk"}
