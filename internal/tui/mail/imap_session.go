package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
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

func (s *IMAPSession) Login(ctx context.Context, username, password string) ([]Summary, error) {
	c, err := s.dialIMAP()
	if err != nil {
		return nil, err
	}

	if err := c.Login(username, password); err != nil {
		c.Logout()
		return nil, fmt.Errorf("invalid credentials: %w", err)
	}

	s.client = c
	s.username = username
	s.submissionAuth = smtp.PlainAuth("", username, password, s.server)

	return s.loadInbox()
}

func (s *IMAPSession) Refresh(ctx context.Context) ([]Summary, error) {
	if s.client == nil {
		return nil, fmt.Errorf("not connected")
	}
	return s.loadInbox()
}

func (s *IMAPSession) loadInbox() ([]Summary, error) {
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
	s.client.UidStore(storeSet, imap.FormatFlagsOp(imap.AddFlags, false), []interface{}{imap.SeenFlag}, nil)

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

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s",
		s.sender(), to, out.Subject, out.Body)

	addr := net.JoinHostPort(s.server, s.submissionPort)
	if err := smtp.SendMail(addr, s.submissionAuth, s.sender(), strings.Split(to, ","), []byte(msg)); err != nil {
		return fmt.Errorf("send failed: %w", err)
	}
	return nil
}

func (s *IMAPSession) Logout(ctx context.Context) error {
	if s.client == nil {
		return nil
	}
	err := s.client.Logout()
	s.client = nil
	return err
}

func (s *IMAPSession) sender() string {
	return s.username
}

// dialIMAP tries IMAPS (993), then IMAP (143) + STARTTLS, then (when insecure)
// plain IMAP. When insecure is false, the server certificate is verified
// against the configured ServerName — no InsecureSkipVerify shortcut.
func (s *IMAPSession) dialIMAP() (*imapclient.Client, error) {
	if s.insecure {
		c, err := imapclient.Dial(net.JoinHostPort(s.server, "143"))
		if err != nil {
			return nil, fmt.Errorf("cannot reach %s:143: %w", s.server, err)
		}
		return c, nil
	}

	tlsCfg := &tls.Config{
		ServerName: s.server,
	}

	if c, err := imapclient.DialTLS(net.JoinHostPort(s.server, "993"), tlsCfg); err == nil {
		return c, nil
	}

	c, err := imapclient.Dial(net.JoinHostPort(s.server, "143"))
	if err == nil {
		if err := c.StartTLS(tlsCfg); err == nil {
			return c, nil
		}
		c.Logout()
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
			buf.ReadFrom(literal)
			return buf.String()
		}
	}
	for _, literal := range msg.Body {
		buf := new(bytes.Buffer)
		buf.ReadFrom(literal)
		return buf.String()
	}
	return ""
}
