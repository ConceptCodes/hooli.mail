package imap

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"

	"hooli.mail/server/internal/auth"
	"hooli.mail/server/internal/models"
	"hooli.mail/server/internal/storage/postgres"

	imap "github.com/emersion/go-imap"
	imapbackend "github.com/emersion/go-imap/backend"
	imapserver "github.com/emersion/go-imap/server"
)

type IMAPBackend struct {
	store *postgres.Store
}

func NewBackend(store *postgres.Store) *IMAPBackend {
	return &IMAPBackend{store: store}
}

func (b *IMAPBackend) Login(connInfo *imap.ConnInfo, username, password string) (imapbackend.User, error) {
	ctx := context.Background()

	u, err := b.store.GetUserByEmail(ctx, username)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	if u == nil {
		return nil, imapbackend.ErrInvalidCredentials
	}

	if err := auth.VerifyPassword(password, u.PasswordHash); err != nil {
		return nil, imapbackend.ErrInvalidCredentials
	}

	return &IMAPUser{
		backend: b,
		user:    u,
		ctx:     ctx,
	}, nil
}

type IMAPUser struct {
	backend *IMAPBackend
	user    *models.User
	ctx     context.Context
}

func (u *IMAPUser) Username() string {
	return u.user.Email
}

func (u *IMAPUser) ListMailboxes(subscribed bool) ([]imapbackend.Mailbox, error) {
	mailboxes, err := u.backend.store.GetMailboxes(u.ctx, u.user.ID)
	if err != nil {
		return nil, err
	}

	var boxes []imapbackend.Mailbox
	for _, mb := range mailboxes {
		boxes = append(boxes, &IMAPMailbox{
			backend: u.backend,
			mailbox: &mb,
			user:    u,
		})
	}
	return boxes, nil
}

func (u *IMAPUser) GetMailbox(name string) (imapbackend.Mailbox, error) {
	mb, err := u.backend.store.GetMailboxByName(u.ctx, u.user.ID, name)
	if err != nil {
		return nil, err
	}
	if mb == nil {
		return nil, imapbackend.ErrNoSuchMailbox
	}
	return &IMAPMailbox{
		backend: u.backend,
		mailbox: mb,
		user:    u,
	}, nil
}

func (u *IMAPUser) CreateMailbox(name string) error {
	_, err := u.backend.store.CreateMailbox(u.ctx, u.user.ID, name)
	return err
}

func (u *IMAPUser) DeleteMailbox(name string) error {
	mb, err := u.backend.store.GetMailboxByName(u.ctx, u.user.ID, name)
	if err != nil {
		return err
	}
	if mb == nil {
		return imapbackend.ErrNoSuchMailbox
	}
	return u.backend.store.DeleteMailbox(u.ctx, mb.ID)
}

func (u *IMAPUser) RenameMailbox(existing, newName string) error {
	mb, err := u.backend.store.GetMailboxByName(u.ctx, u.user.ID, existing)
	if err != nil {
		return err
	}
	if mb == nil {
		return imapbackend.ErrNoSuchMailbox
	}
	return u.backend.store.RenameMailbox(u.ctx, mb.ID, newName)
}

func (u *IMAPUser) Logout() error {
	return nil
}

type IMAPMailbox struct {
	backend *IMAPBackend
	mailbox *models.Mailbox
	user    *IMAPUser
}

func (m *IMAPMailbox) Name() string {
	return m.mailbox.Name
}

func (m *IMAPMailbox) Info() (*imap.MailboxInfo, error) {
	return &imap.MailboxInfo{
		Name: m.mailbox.Name,
	}, nil
}

func (m *IMAPMailbox) Status(items []imap.StatusItem) (*imap.MailboxStatus, error) {
	count, err := m.backend.store.GetEmailCount(m.user.ctx, m.mailbox.ID)
	if err != nil {
		return nil, err
	}

	lastUID, err := m.backend.store.GetLastUID(m.user.ctx, m.mailbox.ID)
	if err != nil {
		return nil, err
	}

	recent := 0
	if count > 0 {
		recent = count
	}

	return &imap.MailboxStatus{
		Name:          m.mailbox.Name,
		Messages:      uint32(count),
		Recent:        uint32(recent),
		Unseen:        uint32(count),
		UidNext:       lastUID + 1,
		UidValidity:   1,
		Flags:         []string{imap.SeenFlag, imap.AnsweredFlag, imap.FlaggedFlag, imap.DeletedFlag, imap.DraftFlag, imap.RecentFlag},
		PermanentFlags: []string{imap.SeenFlag, imap.AnsweredFlag, imap.FlaggedFlag, imap.DeletedFlag, imap.DraftFlag},
	}, nil
}

func (m *IMAPMailbox) SetSubscribed(subscribed bool) error {
	return nil
}

func (m *IMAPMailbox) Check() error {
	return nil
}

func (m *IMAPMailbox) ListMessages(uid bool, seqSet *imap.SeqSet, items []imap.FetchItem, ch chan<- *imap.Message) error {
	defer close(ch)

	emails, err := m.backend.store.GetEmails(m.user.ctx, m.mailbox.ID)
	if err != nil {
		return err
	}

	for _, email := range emails {
		seqNum := email.SeqNum
		var id uint32
		if uid {
			id = uint32(email.ID)
		} else {
			id = seqNum
		}

		if !seqSet.Contains(id) {
			continue
		}

		msg := imap.NewMessage(seqNum, items)
		msg.Uid = uint32(email.ID)
		msg.Flags = email.Flags
		msg.Size = uint32(email.Size)
		msg.InternalDate = email.Date

		for _, item := range items {
			switch item {
			case imap.FetchEnvelope:
				msg.Envelope = m.buildEnvelope(email)
			case imap.FetchBody, imap.FetchBodyStructure:
				if msg.Body == nil {
					msg.Body = make(map[*imap.BodySectionName]imap.Literal)
				}
				sectionName := &imap.BodySectionName{
					BodyPartName: imap.BodyPartName{Specifier: imap.TextSpecifier},
				}
				msg.Body[sectionName] = bytes.NewReader([]byte(email.Body))
				if msg.BodyStructure == nil {
					msg.BodyStructure = &imap.BodyStructure{
						MIMEType:    "text",
						MIMESubType: "plain",
					}
				}
			}
		}

		ch <- msg
	}

	return nil
}

func (m *IMAPMailbox) SearchMessages(uid bool, criteria *imap.SearchCriteria) ([]uint32, error) {
	emails, err := m.backend.store.GetEmails(m.user.ctx, m.mailbox.ID)
	if err != nil {
		return nil, err
	}

	var ids []uint32
	for _, email := range emails {
		var id uint32
		if uid {
			id = uint32(email.ID)
		} else {
			id = email.SeqNum
		}

		if criteria.WithoutFlags != nil {
			hasFlag := false
			for _, f := range email.Flags {
				for _, nf := range criteria.WithoutFlags {
					if strings.EqualFold(f, string(nf)) {
						hasFlag = true
						break
					}
				}
			}
			if hasFlag {
				continue
			}
		}

		ids = append(ids, id)
	}
	return ids, nil
}

func (m *IMAPMailbox) CreateMessage(flags []string, date time.Time, body imap.Literal) error {
	raw, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	content := string(raw)
	subject := ""
	from := ""
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "subject:") {
			subject = strings.TrimSpace(line[8:])
		} else if strings.HasPrefix(lower, "from:") {
			from = strings.TrimSpace(line[5:])
		}
		if subject != "" && from != "" {
			break
		}
	}

	var to []string
	line1 := strings.SplitN(content, "\n", 2)[0]
	if strings.HasPrefix(strings.ToLower(line1), "to:") {
		to = append(to, strings.TrimSpace(line1[3:]))
	}

	allFlags := flags
	if allFlags == nil {
		allFlags = []string{models.FlagRecent}
	}

	_, err = m.backend.store.CreateEmail(m.user.ctx, m.mailbox.ID, from, to, subject, content)
	return err
}

func (m *IMAPMailbox) UpdateMessagesFlags(uid bool, seqSet *imap.SeqSet, operation imap.FlagsOp, flags []string) error {
	emails, err := m.backend.store.GetEmails(m.user.ctx, m.mailbox.ID)
	if err != nil {
		return err
	}

	for _, email := range emails {
		var id uint32
		if uid {
			id = uint32(email.ID)
		} else {
			id = email.SeqNum
		}

		if !seqSet.Contains(id) {
			continue
		}

		newFlags := applyFlagsOp(email.Flags, operation, flags)
		if err := m.backend.store.UpdateFlags(m.user.ctx, email.ID, newFlags); err != nil {
			return err
		}
	}
	return nil
}

func (m *IMAPMailbox) CopyMessages(uid bool, seqSet *imap.SeqSet, destName string) error {
	dest, err := m.backend.store.GetMailboxByName(m.user.ctx, m.user.user.ID, destName)
	if err != nil {
		return err
	}
	if dest == nil {
		return fmt.Errorf("destination mailbox not found: %s", destName)
	}

	emails, err := m.backend.store.GetEmails(m.user.ctx, m.mailbox.ID)
	if err != nil {
		return err
	}

	for _, email := range emails {
		var id uint32
		if uid {
			id = uint32(email.ID)
		} else {
			id = email.SeqNum
		}

		if !seqSet.Contains(id) {
			continue
		}

		_, err := m.backend.store.CreateEmail(m.user.ctx, dest.ID, email.From, email.To, email.Subject, email.Body)
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *IMAPMailbox) Expunge() error {
	emails, err := m.backend.store.GetEmails(m.user.ctx, m.mailbox.ID)
	if err != nil {
		return err
	}

	for _, email := range emails {
		for _, flag := range email.Flags {
			if strings.EqualFold(flag, string(imap.DeletedFlag)) {
				if err := m.backend.store.DeleteEmail(m.user.ctx, email.ID); err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}

func (m *IMAPMailbox) buildEnvelope(email models.Email) *imap.Envelope {
	var to []*imap.Address
	for _, addr := range email.To {
		parts := strings.SplitN(addr, "@", 2)
		mailboxName := parts[0]
		hostName := ""
		if len(parts) > 1 {
			hostName = parts[1]
		}
		to = append(to, &imap.Address{
			PersonalName: mailboxName,
			MailboxName:  mailboxName,
			HostName:     hostName,
		})
	}

	parts := strings.SplitN(email.From, "@", 2)
	mailboxName := parts[0]
	hostName := ""
	if len(parts) > 1 {
		hostName = parts[1]
	}

	return &imap.Envelope{
		Date:    email.Date,
		Subject: email.Subject,
		From:    []*imap.Address{{
			PersonalName: mailboxName,
			MailboxName:  mailboxName,
			HostName:     hostName,
		}},
		To: to,
	}
}

func applyFlagsOp(current []string, op imap.FlagsOp, flags []string) []string {
	flagSet := make(map[string]bool)
	for _, f := range current {
		flagSet[f] = true
	}

	for _, f := range flags {
		switch op {
		case imap.SetFlags:
			flagSet = make(map[string]bool)
			flagSet[f] = true
		case imap.AddFlags:
			flagSet[f] = true
		case imap.RemoveFlags:
			delete(flagSet, f)
		}
	}

	result := make([]string, 0, len(flagSet))
	for f := range flagSet {
		result = append(result, f)
	}
	return result
}

type Server struct {
	srv *imapserver.Server
}

func NewServer(store *postgres.Store, addr string, tlsCfg *tls.Config) *Server {
	backend := NewBackend(store)
	srv := imapserver.New(backend)
	srv.Addr = addr
	srv.AllowInsecureAuth = true
	srv.ErrorLog = log.Default()

	if tlsCfg != nil {
		srv.TLSConfig = tlsCfg
	}

	return &Server{srv: srv}
}

func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.srv.Addr)
	if err != nil {
		return fmt.Errorf("listen imap: %w", err)
	}
	log.Printf("IMAP server listening on %s (TLS: %v)", s.srv.Addr, s.srv.TLSConfig != nil)
	return s.srv.Serve(ln)
}

func (s *Server) Stop() {
	s.srv.Close()
}
