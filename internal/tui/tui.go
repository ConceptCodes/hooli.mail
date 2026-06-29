// Package tui is the terminal mail client. The root model owns navigation
// between six views (login, inbox, message, compose, draft-confirm, drafts)
// and delegates rendering and commands to focused workflow files:
//   - login.go    — authentication form and session.Login command
//   - inbox.go    — email list, date grouping, cursor, and Refresh command
//   - message.go  — message reading, glamour rendering, and Fetch command
//   - compose.go  — compose form, field navigation, Send command, draft data
//   - drafts.go   — saved-drafts list and rendering
//   - helpers.go  — shared layout helpers (truncate, padToHeight, windowLines)
//
// Splitting by workflow gives each view's state transitions and rendering a
// single home. Understanding compose behaviour no longer requires navigating
// unrelated inbox and message-reading code.
package tui

import (
	"context"
	"strings"
	"time"

	"hooli.mail/server/internal/config"
	"hooli.mail/server/internal/tui/mail"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type view int

const (
	loginView view = iota
	inboxView
	messageView
	composeView
	draftConfirmView
	draftsView
)

// model is the Bubble Tea model. It holds view state only: all mail-server
// interaction goes through the mail.Session seam, so this struct carries no
// live sockets and no command mutates it from a goroutine. Commands return
// data via messages; Update is the only place state changes.
type model struct {
	state view

	width  int
	height int

	styles Styles

	server   string
	insecure bool
	username string
	password string

	emailInput    textinput.Model
	passwordInput textinput.Model
	loggedInUser  string

	session mail.Session

	ctx    context.Context
	cancel context.CancelFunc

	emails []mail.Summary
	cursor int
	total  int

	viewing  *mail.Full
	viewport viewport.Model

	composeTo      textinput.Model
	composeCc      textinput.Model
	composeBcc     textinput.Model
	composeSubject textinput.Model
	composeBody    string
	composeCursor  int

	err     error
	loading string

	drafts       []draftData
	draftsCursor int
}

//nolint:revive // unexported-return: returning *model is deliberate — callers use it via tea.Model and the type stays opaque.
func New(server string, insecure bool, cfg config.Config) *model {
	return NewWithSession(mail.NewIMAPSession(server, insecure), server, insecure, cfg)
}

//nolint:revive // unexported-return: returning *model is deliberate — callers use it via tea.Model and the type stays opaque.
func NewWithSession(session mail.Session, server string, insecure bool, cfg config.Config) *model {
	s := NewStyles(cfg)

	ei := textinput.New()
	ei.Placeholder = "you@example.com"
	ei.Focus()
	ei.Width = 36
	ei.Prompt = ""
	ei.PromptStyle = lipgloss.NewStyle()
	ei.TextStyle = s.Primary
	ei.PlaceholderStyle = s.Muted
	ei.Cursor.Style = s.Seal

	pi := textinput.New()
	pi.Placeholder = "password"
	pi.Width = 36
	pi.EchoMode = textinput.EchoPassword
	pi.Prompt = ""
	pi.PromptStyle = lipgloss.NewStyle()
	pi.TextStyle = s.Primary
	pi.PlaceholderStyle = s.Muted
	pi.Cursor.Style = s.Seal

	ct := textinput.New()
	ct.Placeholder = "recipient@example.com"
	ct.Width = 50
	ct.Prompt = ""
	ct.PromptStyle = lipgloss.NewStyle()
	ct.TextStyle = s.Primary
	ct.PlaceholderStyle = s.Muted
	ct.Cursor.Style = s.Seal

	cs := textinput.New()
	cs.Placeholder = "subject"
	cs.Width = 50
	cs.Prompt = ""
	cs.PromptStyle = lipgloss.NewStyle()
	cs.TextStyle = s.Primary
	cs.PlaceholderStyle = s.Muted
	cs.Cursor.Style = s.Seal

	cc := textinput.New()
	cc.Placeholder = "cc@example.com"
	cc.Width = 50
	cc.Prompt = ""
	cc.PromptStyle = lipgloss.NewStyle()
	cc.TextStyle = s.Primary
	cc.PlaceholderStyle = s.Muted
	cc.Cursor.Style = s.Seal

	bcc := textinput.New()
	bcc.Placeholder = "bcc@example.com"
	bcc.Width = 50
	bcc.Prompt = ""
	bcc.PromptStyle = lipgloss.NewStyle()
	bcc.TextStyle = s.Primary
	bcc.PlaceholderStyle = s.Muted
	bcc.Cursor.Style = s.Seal

	vp := viewport.New(80, 20)

	ctx, cancel := context.WithCancel(context.Background())
	return &model{
		state:          loginView,
		styles:         s,
		server:         server,
		insecure:       insecure,
		session:        session,
		ctx:            ctx,
		cancel:         cancel,
		emailInput:     ei,
		passwordInput:  pi,
		composeTo:      ct,
		composeCc:      cc,
		composeBcc:     bcc,
		composeSubject: cs,
		viewport:       vp,
	}
}

func (m *model) Init() tea.Cmd {
	return textinput.Blink
}

func (m *model) Logout() error {
	m.cancel()
	if m.session == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return m.session.Logout(ctx)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport.Width = msg.Width - 4
		m.viewport.Height = msg.Height - 10

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.cancel()
			return m, tea.Quit
		case "ctrl+q":
			if m.state == inboxView || m.state == loginView || m.state == draftsView {
				m.cancel()
				return m, tea.Quit
			}
		case "q":
			if m.state == inboxView && len(m.emails) == 0 {
				m.cancel()
				return m, tea.Quit
			}
			if m.state == draftsView {
				m.state = inboxView
				return m, nil
			}
		case "esc", "escape":
			switch m.state {
			case messageView:
				m.state = inboxView
				m.viewing = nil
				return m, nil
			case composeView:
				hasContent := strings.TrimSpace(m.composeTo.Value()) != "" ||
					strings.TrimSpace(m.composeCc.Value()) != "" ||
					strings.TrimSpace(m.composeBcc.Value()) != "" ||
					strings.TrimSpace(m.composeSubject.Value()) != "" ||
					strings.TrimSpace(m.composeBody) != ""
				if hasContent {
					m.state = draftConfirmView
				} else {
					m.state = inboxView
				}
				return m, nil
			case draftConfirmView:
				m.state = composeView
				return m, nil
			case draftsView:
				m.state = inboxView
				return m, nil
			}
		case "enter":
			switch m.state {
			case loginView:
				if m.emailInput.Focused() {
					m.emailInput.Blur()
					m.passwordInput.Focus()
					return m, nil
				}
				m.username = m.emailInput.Value()
				m.password = m.passwordInput.Value()
				m.loading = "Connecting"
				m.err = nil
				return m, m.login()
			case inboxView:
				if len(m.emails) > 0 && m.cursor >= 0 && m.cursor < len(m.emails) {
					m.state = messageView
					m.viewing = nil
					return m, m.fetchMessage(m.emails[m.cursor].UID)
				}
			case draftsView:
				if len(m.drafts) > 0 {
					d := m.drafts[m.draftsCursor]
					m.composeTo.SetValue(d.To)
					m.composeCc.SetValue(d.Cc)
					m.composeBcc.SetValue(d.Bcc)
					m.composeSubject.SetValue(d.Subject)
					m.composeBody = d.Body
					m.composeCursor = 0
					m.composeTo.Focus()
					m.drafts = append(m.drafts[:m.draftsCursor], m.drafts[m.draftsCursor+1:]...)
					m.state = composeView
					return m, nil
				}
			}
		case "tab":
			switch m.state {
			case loginView:
				if m.emailInput.Focused() {
					m.emailInput.Blur()
					m.passwordInput.Focus()
				} else {
					m.passwordInput.Blur()
					m.emailInput.Focus()
				}
				return m, nil
			case composeView:
				m.advanceComposeField()
				return m, nil
			}
		case "shift+tab":
			if m.state == composeView {
				m.retreatComposeField()
				return m, nil
			}
		case "up", "k":
			if m.state == inboxView && m.cursor > 0 {
				m.cursor--
			}
			if m.state == draftsView && m.draftsCursor > 0 {
				m.draftsCursor--
			}
		case "down", "j":
			if m.state == inboxView && m.cursor < len(m.emails)-1 {
				m.cursor++
			}
			if m.state == draftsView && m.draftsCursor < len(m.drafts)-1 {
				m.draftsCursor++
			}
		case "g":
			if m.state == inboxView {
				m.cursor = 0
			}
			if m.state == draftsView {
				m.draftsCursor = 0
			}
		case "G":
			if m.state == inboxView && len(m.emails) > 0 {
				m.cursor = len(m.emails) - 1
			}
			if m.state == draftsView && len(m.drafts) > 0 {
				m.draftsCursor = len(m.drafts) - 1
			}
		case "r":
			if m.state == inboxView && m.session != nil {
				m.loading = "Refreshing"
				return m, m.refreshInbox()
			}
		case "c":
			if m.state == inboxView {
				m.composeTo.SetValue("")
				m.composeCc.SetValue("")
				m.composeBcc.SetValue("")
				m.composeSubject.SetValue("")
				m.composeBody = ""
				m.composeCursor = 0
				m.composeTo.Focus()
				m.state = composeView
				return m, nil
			}
		case "D":
			if m.state == inboxView && len(m.drafts) > 0 {
				m.state = draftsView
				m.draftsCursor = 0
				return m, nil
			}
		case "d":
			if m.state == draftsView && len(m.drafts) > 0 {
				m.drafts = append(m.drafts[:m.draftsCursor], m.drafts[m.draftsCursor+1:]...)
				if m.draftsCursor >= len(m.drafts) && m.draftsCursor > 0 {
					m.draftsCursor--
				}
				if len(m.drafts) == 0 {
					m.state = inboxView
				}
				return m, nil
			}
		case "ctrl+s":
			if m.state == composeView || m.state == draftConfirmView {
				m.loading = "Sending"
				return m, m.sendMail()
			}
		}

	case errMsg:
		m.loading = ""
		m.err = msg

	case loginSuccess:
		m.loading = ""
		m.err = nil
		m.state = inboxView
		m.loggedInUser = m.username
		m.cursor = 0
		m.emails = msg.emails
		m.total = len(msg.emails)

	case inboxLoaded:
		m.loading = ""
		m.err = nil
		m.emails = msg.emails
		m.total = len(msg.emails)

	case messageLoaded:
		m.loading = ""
		m.viewing = msg.email
		for i := range m.emails {
			if m.emails[i].UID == msg.uid {
				m.emails[i].Seen = true
				break
			}
		}

	case sentMsg:
		m.state = inboxView
		m.loading = "Refreshing"
		return m, m.refreshInbox()
	}

	switch m.state {
	case loginView:
		if m.emailInput.Focused() {
			var cmd tea.Cmd
			m.emailInput, cmd = m.emailInput.Update(msg)
			cmds = append(cmds, cmd)
		} else {
			var cmd tea.Cmd
			m.passwordInput, cmd = m.passwordInput.Update(msg)
			cmds = append(cmds, cmd)
		}

	case composeView:
		switch m.composeCursor {
		case 0:
			var cmd tea.Cmd
			m.composeTo, cmd = m.composeTo.Update(msg)
			cmds = append(cmds, cmd)
		case 1:
			var cmd tea.Cmd
			m.composeCc, cmd = m.composeCc.Update(msg)
			cmds = append(cmds, cmd)
		case 2:
			var cmd tea.Cmd
			m.composeBcc, cmd = m.composeBcc.Update(msg)
			cmds = append(cmds, cmd)
		case 3:
			var cmd tea.Cmd
			m.composeSubject, cmd = m.composeSubject.Update(msg)
			cmds = append(cmds, cmd)
		case 4:
			if keyMsg, ok := msg.(tea.KeyMsg); ok {
				switch keyMsg.Type {
				case tea.KeyRunes:
					m.composeBody += string(keyMsg.Runes)
				case tea.KeySpace:
					m.composeBody += " "
				case tea.KeyBackspace:
					if len(m.composeBody) > 0 {
						runes := []rune(m.composeBody)
						m.composeBody = string(runes[:len(runes)-1])
					}
				case tea.KeyEnter:
					m.composeBody += "\n"
				}
			}
		}

	case messageView:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)

	case draftConfirmView:
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			switch keyMsg.String() {
			case "s":
				m.drafts = append(m.drafts, draftData{
					To:      m.composeTo.Value(),
					Cc:      m.composeCc.Value(),
					Bcc:     m.composeBcc.Value(),
					Subject: m.composeSubject.Value(),
					Body:    m.composeBody,
				})
				m.state = inboxView
				return m, nil
			case "d":
				m.state = inboxView
				return m, nil
			case "k":
				m.state = composeView
				return m, nil
			}
		}
	}

	return m, tea.Batch(cmds...)
}

func (m *model) View() string {
	if m.loading != "" {
		return m.loadingView()
	}

	switch m.state {
	case loginView:
		return m.loginView()
	case inboxView:
		return m.inboxView()
	case messageView:
		return m.messageView()
	case composeView:
		return m.composeView()
	case draftConfirmView:
		return m.renderDraftConfirm()
	case draftsView:
		return m.draftsView()
	}
	return ""
}

// --- shared messages ---

type loginSuccess struct {
	emails []mail.Summary
}

type inboxLoaded struct {
	emails []mail.Summary
}

type messageLoaded struct {
	email *mail.Full
	uid   uint32
}

type sentMsg struct{}

type errMsg struct {
	err error
}

func (e errMsg) Error() string {
	return e.err.Error()
}
