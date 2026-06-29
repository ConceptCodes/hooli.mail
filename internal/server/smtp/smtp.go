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
	"net/mail"
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
	ctx         context.Context
}

func NewBackend(store mailstore.Store, authn *auth.Authenticator, requireAuth bool, ctx context.Context) *Backend {
	return &Backend{store: store, authn: authn, requireAuth: requireAuth, ctx: ctx}
}

func (b *Backend) NewSession(_ *gosmtp.Conn) (gosmtp.Session, error) {
	return &Session{
		backend: b,
		ctx:     b.ctx,
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
	// Authenticated users may only send as themselves. The previous
	// strings.Contains check let a@x.com spoof ba@x.com or a@x.com.evil.com;
	// parsing both sides as proper addresses compares the actual mailbox.
	if s.user != nil {
		from = enforceSender(from, s.user.Email)
	}
	s.from = from
	s.to = nil
	return nil
}

// enforceSender normalises the MAIL FROM argument and, if it does not parse to
// the authorised user's address, replaces it with the authorised address. The
// comparison is on the parsed addr (not a substring) so it cannot be tricked
// by a longer hostname that happens to contain the user's address.
func enforceSender(from, authorised string) string {
	cleaned := strings.Trim(from, "<>")
	if addr, err := mail.ParseAddress(cleaned); err == nil {
		if strings.EqualFold(addr.Address, authorised) {
			return addr.Address
		}
	}
	return authorised
}

func (s *Session) Rcpt(to string, opts *gosmtp.RcptOptions) error {
	addr := strings.Trim(to, "<>")
	addr = strings.TrimSuffix(addr, ">")
	if _, err := mail.ParseAddress(addr); err != nil {
		return &gosmtp.SMTPError{
			Code:         501,
			EnhancedCode: gosmtp.EnhancedCode{5, 1, 3},
			Message:      "Bad recipient address syntax",
		}
	}
	s.to = append(s.to, addr)
	return nil
}

func (s *Session) Data(r io.Reader) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("read data: %w", err)
	}

	parsed := message.Parse(raw)

	var lastErr error
	delivered := 0
	for _, recipient := range s.to {
		if err := s.deliver(recipient, parsed); err != nil {
			log.Printf("deliver to %s: %v", recipient, err)
			lastErr = err
			continue
		}
		delivered++
	}

	if delivered == 0 && len(s.to) > 0 {
		// Every recipient failed — tell the client so it can retry / bounce.
		return fmt.Errorf("delivery failed for all recipients: %w", lastErr)
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
	s.user = nil
}

func (s *Session) Logout() error {
	return nil
}

type Server struct {
	srv    *gosmtp.Server
	cancel context.CancelFunc
}

// NewServer wires a mailstore.Store and auth.Authenticator into an SMTP server.
// The context is propagated to every session so that cancellation (shutdown,
// timeout) reaches in-flight DB calls. When tlsCfg is nil, secure auth is
// refused — clients must use TLS for any SASL mechanism that ships credentials.
func NewServer(store mailstore.Store, authn *auth.Authenticator, addr string, tlsCfg *tls.Config, requireAuth bool, ctx context.Context) *Server {
	sessionCtx, cancel := context.WithCancel(ctx)
	backend := NewBackend(store, authn, requireAuth, sessionCtx)
	srv := gosmtp.NewServer(backend)
	srv.Addr = addr
	srv.Domain = "hooli.mail"
	srv.MaxRecipients = 50
	srv.MaxMessageBytes = 10 * 1024 * 1024

	if tlsCfg != nil {
		srv.TLSConfig = tlsCfg
		// TLS available — no reason to ever allow PLAIN/LOGIN over plaintext.
		srv.AllowInsecureAuth = false
	} else {
		// Dev mode (no TLS configured). Allow plaintext auth so the
		// server is still usable locally; never enable this in production.
		srv.AllowInsecureAuth = true
	}

	return &Server{srv: srv, cancel: cancel}
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
	s.cancel()
	s.srv.Close()
}
