// Package smtp is the SMTP protocol adapter: it turns SMTP sessions into
// delivery calls against a mailstore.Store and authenticates them via an
// auth.Authenticator. It owns only the protocol-to-domain translation — storage
// and credentials live behind their seams.
package smtp

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"strings"

	"hooli.mail/server/internal/auth"
	"hooli.mail/server/internal/mailstore"
	"hooli.mail/server/internal/message"
	"hooli.mail/server/internal/models"

	gosmtp "github.com/emersion/go-smtp"
)

type Backend struct {
	store       mailstore.Store
	authn       *auth.Authenticator
	requireAuth bool
}

func NewBackend(store mailstore.Store, authn *auth.Authenticator, requireAuth bool) *Backend {
	return &Backend{store: store, authn: authn, requireAuth: requireAuth}
}

func (b *Backend) NewSession(_ *gosmtp.Conn) (gosmtp.Session, error) {
	return &Session{
		backend: b,
		ctx:     context.Background(),
	}, nil
}

type Session struct {
	backend *Backend
	ctx     context.Context
	from    string
	to      []string
	user    *models.User
}

func (s *Session) AuthPlain(username, password string) error {
	u, err := s.backend.authn.Verify(s.ctx, username, password)
	if err != nil {
		return err
	}
	s.user = u
	return nil
}

func (s *Session) Mail(from string, opts *gosmtp.MailOptions) error {
	if s.backend.requireAuth && s.user == nil {
		return &gosmtp.SMTPError{
			Code:         530,
			EnhancedCode: gosmtp.EnhancedCode{5, 7, 0},
			Message:      "Authentication required",
		}
	}
	if s.user != nil && !strings.Contains(from, s.user.Email) {
		from = s.user.Email
	}
	s.from = from
	s.to = nil
	return nil
}

func (s *Session) Rcpt(to string, opts *gosmtp.RcptOptions) error {
	addr := strings.TrimPrefix(to, "<")
	addr = strings.TrimSuffix(addr, ">")
	s.to = append(s.to, addr)
	return nil
}

func (s *Session) Data(r io.Reader) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read data: %w", err)
	}

	parsed := message.Parse(raw)

	for _, recipient := range s.to {
		if err := s.deliver(recipient, parsed); err != nil {
			log.Printf("deliver to %s: %v", recipient, err)
		}
	}

	return nil
}

func (s *Session) deliver(recipientEmail string, parsed message.Parsed) error {
	u, err := s.backend.store.GetUserByEmail(s.ctx, recipientEmail)
	if err != nil {
		return fmt.Errorf("get recipient: %w", err)
	}
	if u == nil {
		return fmt.Errorf("user not found: %s", recipientEmail)
	}

	inbox, err := s.backend.store.GetMailboxByName(s.ctx, u.ID, "INBOX")
	if err != nil {
		return fmt.Errorf("get inbox: %w", err)
	}
	if inbox == nil {
		return fmt.Errorf("inbox not found for user %s", recipientEmail)
	}

	to := s.to
	if to == nil {
		to = []string{recipientEmail}
	}

	_, err = s.backend.store.Append(s.ctx, inbox.ID, mailstore.Message{
		From:    s.from,
		To:      to,
		Subject: parsed.Subject,
		Body:    parsed.Body,
	})
	return err
}

func (s *Session) Reset() {
	s.from = ""
	s.to = nil
}

func (s *Session) Logout() error {
	return nil
}

type Server struct {
	srv *gosmtp.Server
}

func NewServer(store mailstore.Store, authn *auth.Authenticator, addr string, tlsCfg *tls.Config, requireAuth bool) *Server {
	backend := NewBackend(store, authn, requireAuth)
	srv := gosmtp.NewServer(backend)
	srv.Addr = addr
	srv.Domain = "hooli.mail"
	srv.AllowInsecureAuth = true
	srv.MaxRecipients = 50
	srv.MaxMessageBytes = 10 * 1024 * 1024

	if tlsCfg != nil {
		srv.TLSConfig = tlsCfg
	}

	return &Server{srv: srv}
}

func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.srv.Addr)
	if err != nil {
		return fmt.Errorf("listen smtp: %w", err)
	}
	log.Printf("SMTP server listening on %s (TLS: %v)", s.srv.Addr, s.srv.TLSConfig != nil)
	return s.srv.Serve(ln)
}

func (s *Server) Stop() {
	s.srv.Close()
}
