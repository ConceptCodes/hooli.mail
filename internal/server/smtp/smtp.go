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
	"hooli.mail/server/internal/storage/postgres"

	gosmtp "github.com/emersion/go-smtp"
)

type Backend struct {
	store       *postgres.Store
	requireAuth bool
}

func NewBackend(store *postgres.Store, requireAuth bool) *Backend {
	return &Backend{store: store, requireAuth: requireAuth}
}

func (b *Backend) NewSession(_ *gosmtp.Conn) (gosmtp.Session, error) {
	return &Session{
		backend: b,
		ctx:     context.Background(),
	}, nil
}

type Session struct {
	backend  *Backend
	ctx      context.Context
	from     string
	to       []string
	user     *postgresUser
}

type postgresUser struct {
	id    int64
	email string
}

func (s *Session) AuthPlain(username, password string) error {
	u, err := s.backend.store.GetUserByEmail(s.ctx, username)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	if u == nil {
		return auth.ErrInvalidCredentials
	}

	if err := auth.VerifyPassword(password, u.PasswordHash); err != nil {
		return auth.ErrInvalidCredentials
	}

	s.user = &postgresUser{id: u.ID, email: u.Email}
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
	if s.user != nil && !strings.Contains(from, s.user.email) {
		from = s.user.email
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

	body := string(raw)

	subject := ""
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		if strings.HasPrefix(strings.ToLower(line), "subject:") {
			subject = strings.TrimSpace(line[8:])
			break
		}
	}

	for _, recipient := range s.to {
		if err := s.deliver(recipient, subject, body); err != nil {
			log.Printf("deliver to %s: %v", recipient, err)
		}
	}

	return nil
}

func (s *Session) deliver(recipientEmail, subject, body string) error {
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

	_, err = s.backend.store.CreateEmail(s.ctx, inbox.ID, s.from, to, subject, body)
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

func NewServer(store *postgres.Store, addr string, tlsCfg *tls.Config, requireAuth bool) *Server {
	backend := NewBackend(store, requireAuth)
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
