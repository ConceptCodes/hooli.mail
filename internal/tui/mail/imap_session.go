package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/smtp"
	"strings"
	"time"

	imap "github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"
)

// IMAPSession is the real client adapter: IMAP (143 STARTTLS / 993 TLS) for
// reading, SMTP submission (587) for sending. It implements Session.
type IMAPSession struct {
	server         string
	insecure       bool
	submissionPort string
	username       string
	submissionAuth smtp.Auth
	client         *imapclient.Client
}

// NewIMAPSession builds a Session backed by go-imap + net/smtp. The submission
// port defaults to 587 and may be overridden for testing or non-standard deploys.
func NewIMAPSession(server string, insecure bool) *IMAPSession {
	return &IMAPSession{server: server, insecure: insecure, submissionPort: "587"}
}

// withTimeout returns a context bounded by the given timeout, cancelled when
// the caller's ctx is cancelled. The default timeout protects the TUI from
// hanging forever on an unresponsive server even when the caller passes a
// non-cancellable context (e.g. context.Background()).
func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok {
		// Caller already set a deadline; keep the earlier of the two.
		if time.Until(deadline) < d {
			return context.WithCancel(ctx)
		}
	}
	return context.WithTimeout(ctx, d)
}

func (s *IMAPSession) Login(ctx context.Context, username, password string) ([]Summary, error) {
	c, err := s.dialIMAP(ctx)
	if err != nil {
		return nil, err
	}

	if err := c.Login(username, password); err != nil {
		// Best-effort teardown on a failed login; the connection is unusable
		// either way and we don't want to leak it.
		_ = c.Logout()
		return nil, fmt.Errorf("invalid credentials: %w", err)
	}

	s.client = c
	s.username = username
	s.submissionAuth = smtp.PlainAuth("", username, password, s.server)

	return s.loadInbox(ctx)
}

func (s *IMAPSession) Refresh(ctx context.Context) ([]Summary, error) {
	if s.client == nil {
		return nil, fmt.Errorf("not connected")
	}
	return s.loadInbox(ctx)
}

func (s *IMAPSession) loadInbox(ctx context.Context) ([]Summary, error) {
	mbox, err := s.client.Select("INBOX", false)
	if err != nil {
		return nil, fmt.Errorf("cannot open inbox: %w", err)
	}
	if mbox.Messages == 0 {
		return nil, nil
	}

	seqSet := new(imap.SeqSet)
	seqSet.AddRange(1, mbox.Messages)

	messages := make(chan *imap.Message, 10)
	done := make(chan error, 1)
	go func() {
		done <- s.client.Fetch(seqSet, []imap.FetchItem{imap.FetchEnvelope, imap.FetchFlags}, messages)
	}()

	var summaries []Summary
	for msg := range messages {
		summaries = append(summaries, toSummary(msg))
	}
	if err := <-done; err != nil {
		return nil, fmt.Errorf("cannot load inbox: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return summaries, nil
}

func (s *IMAPSession) Fetch(ctx context.Context, uid uint32) (*Full, error) {
	if s.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uid)

	section := &imap.BodySectionName{}
	section.Specifier = imap.TextSpecifier
	items := []imap.FetchItem{section.FetchItem(), imap.FetchEnvelope, imap.FetchFlags}

	messages := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() {
		done <- s.client.UidFetch(seqSet, items, messages)
	}()

	msg := <-messages
	if err := <-done; err != nil {
		return nil, fmt.Errorf("cannot fetch message: %w", err)
	}
	if msg == nil {
		return nil, fmt.Errorf("message not found")
	}

	body := extractBody(msg)

	var to []string
	if msg.Envelope != nil {
		for _, addr := range msg.Envelope.To {
			to = append(to, addr.MailboxName+"@"+addr.HostName)
		}
	}

	from := ""
	subject := ""
	if msg.Envelope != nil {
		subject = msg.Envelope.Subject
		if len(msg.Envelope.From) > 0 {
			f := msg.Envelope.From[0]
			from = f.MailboxName + "@" + f.HostName
			if f.PersonalName != "" {
				from = f.PersonalName + " <" + from + ">"
			}
		}
	}

	storeSet := new(imap.SeqSet)
	storeSet.AddNum(uid)
	if err := s.client.UidStore(storeSet, imap.FormatFlagsOp(imap.AddFlags, false), []interface{}{imap.SeenFlag}, nil); err != nil {
		// Non-fatal: we still show the message, but log the cause so the
		// \Seen update isn't silently dropped.
		log.Printf("imap: marking %d \\Seen: %v", uid, err)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return &Full{
		From:    from,
		To:      to,
		Subject: subject,
		Body:    strings.TrimSpace(body),
		Date:    msg.Envelope.Date,
	}, nil
}

func (s *IMAPSession) Send(ctx context.Context, out Outgoing) error {
	if s.submissionAuth == nil {
		return fmt.Errorf("not authenticated")
	}
	to := strings.TrimSpace(out.To)
	if to == "" {
		return fmt.Errorf("recipient required")
	}

	// Build message headers. Bcc is envelope-only — it must NOT appear
	// in the message headers, otherwise every recipient can see who was
	// blind-carbon-copied.
	headers := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n",
		s.sender(), to, out.Subject)
	if cc := strings.TrimSpace(out.Cc); cc != "" {
		headers += fmt.Sprintf("Cc: %s\r\n", cc)
	}
	msg := headers + "\r\n" + out.Body

	// Collect all envelope recipients: To + Cc + Bcc. Every one needs a
	// RCPT TO in the SMTP transaction.
	recipients := splitAndTrim(to)
	if cc := strings.TrimSpace(out.Cc); cc != "" {
		recipients = append(recipients, splitAndTrim(cc)...)
	}
	if bcc := strings.TrimSpace(out.Bcc); bcc != "" {
		recipients = append(recipients, splitAndTrim(bcc)...)
	}

	addr := net.JoinHostPort(s.server, s.submissionPort)
	done := make(chan error, 1)
	go func() {
		done <- smtp.SendMail(addr, s.submissionAuth, s.sender(), recipients, []byte(msg))
	}()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("send failed: %w", err)
		}
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// splitAndTrim splits a comma-separated address list and trims whitespace
// from each element, dropping empties.
func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (s *IMAPSession) Logout(ctx context.Context) error {
	if s.client == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- s.client.Logout() }()
	select {
	case err := <-done:
		s.client = nil
		return err
	case <-time.After(5 * time.Second):
		// Server didn't reply to LOGOUT — drop the connection so the TUI
		// never hangs on quit. The client is unusable either way.
		s.client = nil
		return nil
	case <-ctx.Done():
		s.client = nil
		return ctx.Err()
	}
}

func (s *IMAPSession) sender() string {
	return s.username
}

// ctxDialer adapts a *net.Dialer + context to the imapclient.Dialer
// interface, so dial cancellation honours ctx.Done() rather than just a
// static Timeout.
type ctxDialer struct {
	ctx context.Context
	d   *net.Dialer
}

func (c ctxDialer) Dial(network, addr string) (net.Conn, error) {
	return c.d.DialContext(c.ctx, network, addr)
}

// dialIMAP tries IMAPS (993), then IMAP (143) + STARTTLS, then (when insecure)
// plain IMAP. When insecure is false, the server certificate is verified
// against the configured ServerName — no InsecureSkipVerify shortcut.
//
// The dial itself is bounded by ctx; if the caller's context has no deadline
// we apply a 30s default so an unreachable host can never wedge the TUI.
func (s *IMAPSession) dialIMAP(ctx context.Context) (*imapclient.Client, error) {
	dialCtx, cancel := withTimeout(ctx, 30*time.Second)
	defer cancel()

	d := ctxDialer{ctx: dialCtx, d: &net.Dialer{}}

	if s.insecure {
		c, err := imapclient.DialWithDialer(d, net.JoinHostPort(s.server, "143"))
		if err != nil {
			return nil, fmt.Errorf("cannot reach %s:143: %w", s.server, err)
		}
		return c, nil
	}

	tlsCfg := &tls.Config{
		ServerName: s.server,
	}

	if c, err := imapclient.DialWithDialerTLS(d, net.JoinHostPort(s.server, "993"), tlsCfg); err == nil {
		return c, nil
	} else if ctxErr := dialCtx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("cannot connect to %s:993: %w", s.server, ctxErr)
	}

	c, err := imapclient.DialWithDialer(d, net.JoinHostPort(s.server, "143"))
	if err == nil {
		if err := c.StartTLS(tlsCfg); err == nil {
			return c, nil
		}
		// STARTTLS failed on the plain-text connection — drop it before we
		// fall through to the overall error.
		_ = c.Logout()
	}

	return nil, fmt.Errorf("cannot connect to %s (IMAP 993/143)", s.server)
}

// toSummary converts a fetched IMAP message into an inbox row. This used to be
// inlined twice (login and refresh); it now lives in one place.
func toSummary(msg *imap.Message) Summary {
	seen := false
	for _, flag := range msg.Flags {
		if flag == imap.SeenFlag {
			seen = true
			break
		}
	}

	from := ""
	subject := ""
	var date time.Time
	if msg.Envelope != nil {
		subject = msg.Envelope.Subject
		if len(msg.Envelope.From) > 0 {
			f := msg.Envelope.From[0]
			from = f.MailboxName + "@" + f.HostName
			if f.PersonalName != "" {
				from = f.PersonalName
			}
		}
		date = msg.Envelope.Date
	}

	return Summary{
		UID:     msg.Uid,
		From:    from,
		Subject: subject,
		Date:    date,
		Seen:    seen,
	}
}

func extractBody(msg *imap.Message) string {
	for sectionName, literal := range msg.Body {
		if sectionName.Specifier == imap.TextSpecifier || sectionName.Specifier == imap.EntireSpecifier {
			buf := new(bytes.Buffer)
			if _, err := buf.ReadFrom(literal); err != nil {
				log.Printf("imap: extracting body section: %v", err)
				continue
			}
			return buf.String()
		}
	}
	for _, literal := range msg.Body {
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(literal); err != nil {
			log.Printf("imap: extracting body fallback: %v", err)
			continue
		}
		return buf.String()
	}
	return ""
}
