// Package smtp is the SMTP protocol adapter: it turns SMTP sessions into
// delivery calls against a delivery.Service and authenticates them via an
// auth.Authenticator. It owns only the protocol-to-domain translation —
// delivery policy, storage, and credentials live behind their seams.
package smtp

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/mail"
	"strings"

	"hooli.mail/server/internal/auth"
	"hooli.mail/server/internal/delivery"
	"hooli.mail/server/internal/logger"
	"hooli.mail/server/internal/mailstore"
	"hooli.mail/server/internal/models"

	gosmtp "github.com/emersion/go-smtp"
)

type Backend struct {
	delivery    *delivery.Service
	authn       *auth.Authenticator
	requireAuth bool
	ctx         context.Context
}

func NewBackend(ctx context.Context, store mailstore.Store, authn *auth.Authenticator, requireAuth bool) *Backend {
	return &Backend{
		delivery:    delivery.New(store),
		authn:       authn,
		requireAuth: requireAuth,
		ctx:         ctx,
	}
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

	results := s.backend.delivery.Deliver(s.ctx, raw, s.to)

	var lastErr error
	delivered := 0
	for _, r := range results {
		if r.Err != nil {
			logger.Warn("delivery failed for recipient", "recipient", r.Recipient, "error", r.Err)
			lastErr = r.Err
			continue
		}
		delivered++
	}

	if delivered == 0 && len(s.to) > 0 {
		return fmt.Errorf("delivery failed for all recipients: %w", lastErr)
	}
	return nil
}

// Reset aborts the current mail transaction. Per RFC 5321 §4.1.1.5, RSET
// clears the MAIL FROM / RCPT TO state and the data buffer — it must NOT
// invalidate the AUTHENTICATED state. Clearing s.user here previously let an
// authenticated client who sent RSET keep submitting without re-AUTH, but a
// subsequent MAIL FROM on a requireAuth backend would have been rejected as
// unauthenticated, which is wrong: RSET is a transaction reset, not a logout.
func (s *Session) Reset() {
	s.from = ""
	s.to = nil
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
func NewServer(ctx context.Context, store mailstore.Store, authn *auth.Authenticator, addr string, tlsCfg *tls.Config, requireAuth bool) *Server {
	sessionCtx, cancel := context.WithCancel(ctx)
	backend := NewBackend(sessionCtx, store, authn, requireAuth)
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
	logger.Info("SMTP server listening", "addr", s.srv.Addr, "tls", s.srv.TLSConfig != nil)
	return s.srv.Serve(ln)
}

func (s *Server) Stop() {
	s.cancel()
	// go-smtp's Close errors only on double-close — safe to ignore during
	// shutdown.
	_ = s.srv.Close()
}
