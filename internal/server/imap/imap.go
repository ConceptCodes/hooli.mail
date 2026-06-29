// Package imap is the IMAP protocol adapter. It translates go-imap backend
// calls into operations on a mailstore.Store, authenticating via an
// auth.Authenticator. Mailbox semantics that were previously reassembled here
// by looping over CRUD reads/writes — status counts, expunge, copy — now live
// behind single Store calls.
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
	"hooli.mail/server/internal/mailstore"
	"hooli.mail/server/internal/message"
	"hooli.mail/server/internal/models"

	imap "github.com/emersion/go-imap"
	imapbackend "github.com/emersion/go-imap/backend"
	imapserver "github.com/emersion/go-imap/server"
)

type IMAPBackend struct {
	store mailstore.Store
	authn *auth.Authenticator
	ctx   context.Context
}

func NewBackend(ctx context.Context, store mailstore.Store, authn *auth.Authenticator) *IMAPBackend {
	return &IMAPBackend{store: store, authn: authn, ctx: ctx}
}

func (b *IMAPBackend) Login(connInfo *imap.ConnInfo, username, password string) (imapbackend.User, error) {
	u, err := b.authn.Verify(b.ctx, username, password)
	if err != nil {
		return nil, imapbackend.ErrInvalidCredentials
	}

	return &IMAPUser{
		backend: b,
		user:    u,
		ctx:     b.ctx,
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
	for i := range mailboxes {
		mb := mailboxes[i]
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
	st, err := m.backend.store.Status(m.user.ctx, m.mailbox.ID)
	if err != nil {
		return nil, err
	}

	return &imap.MailboxStatus{
		Name:           m.mailbox.Name,
		Messages:       st.Messages,
		Recent:         st.Recent,
		Unseen:         st.Unseen,
		UidNext:        st.UIDNext,
		UidValidity:    st.UIDValidity,
		Flags:          []string{imap.SeenFlag, imap.AnsweredFlag, imap.FlaggedFlag, imap.DeletedFlag, imap.DraftFlag, imap.RecentFlag},
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

	emails, err := m.backend.store.List(m.user.ctx, m.mailbox.ID)
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

		msg := imap.NewMessage(email.SeqNum, items)
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
	emails, err := m.backend.store.List(m.user.ctx, m.mailbox.ID)
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

		if !m.matchSearch(email, criteria) {
			continue
		}

		ids = append(ids, id)
	}
	return ids, nil
}

// matchSearch evaluates the subset of imap.SearchCriteria that we support
// in-memory: WithFlags / WithoutFlags, Since / Before (internal date),
// Larger / Smaller (octets), and substring matches on From/To/Subject/Body.
// Criteria we don't model (SentSince, Not, Or, SeqNum) are treated as "match"
// so they fail open rather than silently hiding messages.
func (m *IMAPMailbox) matchSearch(email models.Email, c *imap.SearchCriteria) bool {
	if !matchFlags(email.Flags, c.WithFlags, c.WithoutFlags) {
		return false
	}
	if !matchDateRange(email.Date, c.Since, c.Before) {
		return false
	}
	if !matchSize(email.Size, c.Larger, c.Smaller) {
		return false
	}
	for _, needle := range c.Body {
		if !strings.Contains(strings.ToLower(email.Body), strings.ToLower(needle)) {
			return false
		}
	}
	for _, needle := range c.Text {
		lower := strings.ToLower(needle)
		if !strings.Contains(strings.ToLower(email.From), lower) &&
			!strings.Contains(strings.ToLower(email.Subject), lower) &&
			!strings.Contains(strings.ToLower(email.Body), lower) &&
			!containsAny(email.To, lower) {
			return false
		}
	}
	for header, values := range c.Header {
		switch strings.ToLower(header) {
		case "from":
			for _, v := range values {
				if !strings.Contains(strings.ToLower(email.From), strings.ToLower(v)) {
					return false
				}
			}
		case "to":
			for _, v := range values {
				if !containsAny(email.To, strings.ToLower(v)) {
					return false
				}
			}
		case "subject":
			for _, v := range values {
				if !strings.Contains(strings.ToLower(email.Subject), strings.ToLower(v)) {
					return false
				}
			}
		}
	}
	return true
}

func matchFlags(flags []string, with, without []string) bool {
	has := make(map[string]bool, len(flags))
	for _, f := range flags {
		has[strings.ToLower(f)] = true
	}
	for _, f := range with {
		if !has[strings.ToLower(string(f))] {
			return false
		}
	}
	for _, f := range without {
		if has[strings.ToLower(string(f))] {
			return false
		}
	}
	return true
}

func matchDateRange(t time.Time, since, before time.Time) bool {
	if !since.IsZero() && t.Before(since) {
		return false
	}
	if !before.IsZero() && !t.Before(before) {
		return false
	}
	return true
}

func matchSize(size int, larger, smaller uint32) bool {
	if larger > 0 && uint32(size) <= larger {
		return false
	}
	if smaller > 0 && uint32(size) >= smaller {
		return false
	}
	return true
}

func containsAny(addrs []string, needle string) bool {
	for _, a := range addrs {
		if strings.Contains(strings.ToLower(a), needle) {
			return true
		}
	}
	return false
}

func (m *IMAPMailbox) CreateMessage(flags []string, date time.Time, body imap.Literal) error {
	raw, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	parsed := message.Parse(raw)

	_, err = m.backend.store.Append(m.user.ctx, m.mailbox.ID, mailstore.Message{
		From:    parsed.From,
		To:      parsed.To,
		Subject: parsed.Subject,
		Body:    parsed.Body,
		Flags:   flags,
	})
	return err
}

func (m *IMAPMailbox) UpdateMessagesFlags(uid bool, seqSet *imap.SeqSet, operation imap.FlagsOp, flags []string) error {
	emails, err := m.backend.store.List(m.user.ctx, m.mailbox.ID)
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
		if err := m.backend.store.SetFlags(m.user.ctx, email.ID, newFlags); err != nil {
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

	emails, err := m.backend.store.List(m.user.ctx, m.mailbox.ID)
	if err != nil {
		return err
	}

	var ids []int64
	for _, email := range emails {
		var id uint32
		if uid {
			id = uint32(email.ID)
		} else {
			id = email.SeqNum
		}
		if seqSet.Contains(id) {
			ids = append(ids, email.ID)
		}
	}

	return m.backend.store.Copy(m.user.ctx, m.mailbox.ID, ids, dest.ID)
}

func (m *IMAPMailbox) Expunge() error {
	_, err := m.backend.store.Expunge(m.user.ctx, m.mailbox.ID)
	return err
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
		From: []*imap.Address{{
			PersonalName: mailboxName,
			MailboxName:  mailboxName,
			HostName:     hostName,
		}},
		To: to,
	}
}

// applyFlagsOp encodes IMAP STORE flag operations (set/add/remove) against a
// flag set. It is IMAP semantics, not storage, so it stays in the protocol
// adapter rather than the Store.
func applyFlagsOp(current []string, op imap.FlagsOp, flags []string) []string {
	flagSet := make(map[string]bool)
	for _, f := range current {
		flagSet[f] = true
	}

	switch op {
	case imap.SetFlags:
		// Replace is a single atomic reset, not a per-flag mutation: doing it
		// inside the loop would discard every flag except the last one.
		flagSet = make(map[string]bool, len(flags))
		for _, f := range flags {
			flagSet[f] = true
		}
	case imap.AddFlags:
		for _, f := range flags {
			flagSet[f] = true
		}
	case imap.RemoveFlags:
		for _, f := range flags {
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
	srv         *imapserver.Server
	cancel      context.CancelFunc
	implicitTLS bool
}

// NewServer wires a mailstore.Store and auth.Authenticator into an IMAP server.
// The context is propagated to every backend user so cancellation (shutdown,
// timeout) reaches in-flight DB calls. When tlsCfg is nil, AllowInsecureAuth
// is left false so no SASL PLAIN/LOGIN is accepted over plaintext.
//
// implicitTLS controls how TLS is presented on the listening socket. When false
// (the IMAP/143 default), the listener is plaintext and TLS is offered via
// STARTTLS. When true (the IMAPS/993 form), the listener itself is wrapped in
// tls.Listen so every connection is TLS from the first byte — RFC 8314's
// "implicit TLS". Setting srv.TLSConfig alone does not do this; go-imap would
// otherwise accept plaintext on 993 and only upgrade inside the protocol.
func NewServer(ctx context.Context, store mailstore.Store, authn *auth.Authenticator, addr string, tlsCfg *tls.Config, implicitTLS bool) *Server {
	sessionCtx, cancel := context.WithCancel(ctx)
	backend := NewBackend(sessionCtx, store, authn)
	srv := imapserver.New(backend)
	srv.Addr = addr
	srv.AllowInsecureAuth = tlsCfg == nil
	srv.ErrorLog = log.Default()

	if tlsCfg != nil {
		srv.TLSConfig = tlsCfg
	}

	return &Server{srv: srv, cancel: cancel, implicitTLS: implicitTLS}
}

func (s *Server) Start() error {
	var ln net.Listener
	var err error
	if s.implicitTLS {
		if s.srv.TLSConfig == nil {
			return fmt.Errorf("listen imap: implicitTLS requested without TLS config")
		}
		ln, err = tls.Listen("tcp", s.srv.Addr, s.srv.TLSConfig)
		if err != nil {
			return fmt.Errorf("listen imaps: %w", err)
		}
		log.Printf("IMAPS server listening on %s (implicit TLS)", s.srv.Addr)
	} else {
		ln, err = net.Listen("tcp", s.srv.Addr)
		if err != nil {
			return fmt.Errorf("listen imap: %w", err)
		}
		log.Printf("IMAP server listening on %s (STARTTLS: %v)", s.srv.Addr, s.srv.TLSConfig != nil)
	}
	return s.srv.Serve(ln)
}

func (s *Server) Stop() {
	s.cancel()
	// Close terminates active connections and releases the listener. The
	// returned error is only non-nil on double-close or already-closed
	// listeners, neither of which is actionable here — we're shutting down.
	_ = s.srv.Close()
}
