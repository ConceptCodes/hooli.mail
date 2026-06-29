// Package memory is an in-memory mailstore.Store adapter. It exists for two
// reasons: it makes the mailstore seam real (two adapters = a real seam, not a
// hypothetical one), and it lets the protocol logic be exercised without a
// PostgreSQL instance.
//
// It is not a production store: data lives in process, IDs are assigned from a
// monotonic counter, and there are no transactions. Correctness of the mailbox
// semantics it shares with the postgres adapter is what the tests pin down.
package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"hooli.mail/server/internal/mailbox"
	"hooli.mail/server/internal/mailstore"
	"hooli.mail/server/internal/models"
)

type Store struct {
	mu struct {
		sync.Mutex
		users     map[int64]*models.User
		mailboxes map[int64]*models.Mailbox
		emails    map[int64]*models.Email
	}
	nextID int64
}

var _ mailstore.Store = (*Store)(nil)

func New() *Store {
	s := &Store{nextID: 0}
	s.mu.users = make(map[int64]*models.User)
	s.mu.mailboxes = make(map[int64]*models.Mailbox)
	s.mu.emails = make(map[int64]*models.Email)
	return s
}

func (s *Store) id() int64 {
	s.nextID++
	return s.nextID
}

// cloneUser / cloneMailbox / cloneEmail return deep copies so the store never
// hands out a pointer into its own map. Without these, a caller could mutate
// a stored record (or its slices) outside the mutex — a data race. Slice
// fields (To, Flags) are copied element-wise; scalars ride along.
func cloneUser(u *models.User) *models.User {
	if u == nil {
		return nil
	}
	c := *u
	return &c
}

func cloneMailbox(mb *models.Mailbox) *models.Mailbox {
	if mb == nil {
		return nil
	}
	c := *mb
	return &c
}

func cloneEmail(e *models.Email) *models.Email {
	if e == nil {
		return nil
	}
	c := *e
	if e.To != nil {
		c.To = append([]string(nil), e.To...)
	}
	if e.Cc != nil {
		c.Cc = append([]string(nil), e.Cc...)
	}
	if e.Flags != nil {
		c.Flags = append([]string(nil), e.Flags...)
	}
	if e.Raw != nil {
		c.Raw = append([]byte(nil), e.Raw...)
	}
	return &c
}

func (s *Store) CreateUser(_ context.Context, email, passwordHash string) (*models.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, u := range s.mu.users {
		if u.Email == email {
			return nil, fmt.Errorf("create user: email already exists")
		}
	}

	u := &models.User{ID: s.id(), Email: email, PasswordHash: passwordHash, CreatedAt: time.Now()}
	s.mu.users[u.ID] = u

	for _, name := range models.DefaultMailboxes {
		mb := &models.Mailbox{ID: s.id(), UserID: u.ID, Name: name, CreatedAt: time.Now()}
		s.mu.mailboxes[mb.ID] = mb
	}
	return cloneUser(u), nil
}

func (s *Store) GetUserByEmail(_ context.Context, email string) (*models.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.mu.users {
		if u.Email == email {
			return cloneUser(u), nil
		}
	}
	return nil, nil
}

func (s *Store) CreateMailbox(_ context.Context, userID int64, name string) (*models.Mailbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, mb := range s.mu.mailboxes {
		if mb.UserID == userID && mb.Name == name {
			return nil, fmt.Errorf("create mailbox: already exists")
		}
	}
	mb := &models.Mailbox{ID: s.id(), UserID: userID, Name: name, CreatedAt: time.Now()}
	s.mu.mailboxes[mb.ID] = mb
	return cloneMailbox(mb), nil
}

func (s *Store) GetMailboxes(_ context.Context, userID int64) ([]models.Mailbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []models.Mailbox
	for _, mb := range s.mu.mailboxes {
		if mb.UserID == userID {
			// mb is a value (range copies the *models.Mailbox pointer), but
			// the slice still aliases the same struct. The deref below makes
			// a shallow copy, which is fine — Mailbox has no slice fields.
			out = append(out, *mb)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) GetMailboxByName(_ context.Context, userID int64, name string) (*models.Mailbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, mb := range s.mu.mailboxes {
		if mb.UserID == userID && mb.Name == name {
			return cloneMailbox(mb), nil
		}
	}
	return nil, nil
}

func (s *Store) DeleteMailbox(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.mu.mailboxes, id)
	return nil
}

func (s *Store) RenameMailbox(_ context.Context, id int64, newName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if mb, ok := s.mu.mailboxes[id]; ok {
		mb.Name = newName
	}
	return nil
}

func (s *Store) Status(_ context.Context, mailboxID int64) (mailstore.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := mailstore.Status{UIDValidity: 1}
	var maxID int64
	for _, e := range s.mu.emails {
		if e.MailboxID != mailboxID {
			continue
		}
		st.Messages++
		if hasFlag(e.Flags, models.FlagRecent) {
			st.Recent++
		}
		if !hasFlag(e.Flags, models.FlagSeen) {
			st.Unseen++
		}
		if e.ID > maxID {
			maxID = e.ID
		}
	}
	st.UIDNext = uint32(maxID) + 1
	return st, nil
}

func (s *Store) Append(_ context.Context, mailboxID int64, msg mailstore.Message) (*models.Email, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	flags := msg.Flags
	if len(flags) == 0 {
		flags = []string{models.FlagRecent}
	}
	p := msg.Parsed
	e := &models.Email{
		ID:        s.id(),
		MailboxID: mailboxID,
		From:      p.From,
		To:        p.To,
		Cc:        p.Cc,
		Subject:   p.Subject,
		Body:      p.Body,
		Raw:       p.Raw,
		MessageID: p.MessageID,
		InReplyTo: p.InReplyTo,
		Flags:     append([]string(nil), flags...),
		Date:      time.Now(),
		Size:      p.Size,
	}
	s.mu.emails[e.ID] = e
	return cloneEmail(e), nil
}

func (s *Store) List(_ context.Context, mailboxID int64) ([]models.Email, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []models.Email
	for _, e := range s.mu.emails {
		if e.MailboxID == mailboxID {
			c := *e
			if e.To != nil {
				c.To = append([]string(nil), e.To...)
			}
			if e.Cc != nil {
				c.Cc = append([]string(nil), e.Cc...)
			}
			if e.Flags != nil {
				c.Flags = append([]string(nil), e.Flags...)
			}
			if e.Raw != nil {
				c.Raw = append([]byte(nil), e.Raw...)
			}
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.After(out[j].Date) })
	for i := range out {
		out[i].SeqNum = uint32(i + 1)
	}
	return out, nil
}

func (s *Store) SetFlags(_ context.Context, emailID int64, flags []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.mu.emails[emailID]; ok {
		e.Flags = append([]string(nil), flags...)
	}
	return nil
}

func (s *Store) Search(_ context.Context, mailboxID int64, criteria mailbox.SearchCriteria) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var ids []int64
	for _, e := range s.mu.emails {
		if e.MailboxID != mailboxID {
			continue
		}
		if mailbox.Match(*e, criteria) {
			ids = append(ids, e.ID)
		}
	}
	return ids, nil
}

func (s *Store) UpdateFlags(_ context.Context, mailboxID int64, ids []int64, op mailbox.FlagOperation, flags []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	want := make(map[int64]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	for _, e := range s.mu.emails {
		if e.MailboxID != mailboxID || !want[e.ID] {
			continue
		}
		e.Flags = mailbox.ApplyFlags(e.Flags, op, flags)
	}
	return nil
}

func (s *Store) DeleteMessage(_ context.Context, emailID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.mu.emails, emailID)
	return nil
}

func (s *Store) Expunge(_ context.Context, mailboxID int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, e := range s.mu.emails {
		if e.MailboxID == mailboxID && hasFlag(e.Flags, models.FlagDeleted) {
			delete(s.mu.emails, id)
			n++
		}
	}
	return n, nil
}

func (s *Store) Copy(_ context.Context, srcMailboxID int64, ids []int64, destMailboxID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	want := make(map[int64]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	for _, e := range s.mu.emails {
		if e.MailboxID != srcMailboxID || !want[e.ID] {
			continue
		}
		copy := *e
		copy.ID = s.id()
		copy.MailboxID = destMailboxID
		copy.Flags = append([]string(nil), e.Flags...)
		if e.To != nil {
			copy.To = append([]string(nil), e.To...)
		}
		if e.Cc != nil {
			copy.Cc = append([]string(nil), e.Cc...)
		}
		if e.Raw != nil {
			copy.Raw = append([]byte(nil), e.Raw...)
		}
		copy.Date = time.Now()
		s.mu.emails[copy.ID] = &copy
	}
	return nil
}

func hasFlag(flags []string, flag string) bool {
	for _, f := range flags {
		if f == flag {
			return true
		}
	}
	return false
}
