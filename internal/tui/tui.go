package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"hooli.mail/server/internal/config"
	"hooli.mail/server/internal/tui/mail"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

type view int

const (
	loginView view = iota
	inboxView
	messageView
	composeView
	draftConfirmView // "save draft / discard / keep writing" modal over compose
	draftsView       // list of saved drafts
)

type dateGroup int

const (
	groupToday dateGroup = iota
	groupYesterday
	groupThisWeek
	groupEarlier
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

	// ctx is the root context for all session calls. It is cancelled on quit
	// so any in-flight IMAP/SMTP operation returns promptly instead of
	// hanging forever against an unresponsive server.
	ctx    context.Context
	cancel context.CancelFunc

	emails []mail.Summary
	cursor int
	total  int

	viewing  *mail.Full
	viewport viewport.Model

	composeTo      textinput.Model
	composeSubject textinput.Model
	composeBody    string
	composeCursor  int

	err     error
	loading string

	// in-session drafts (lost on quit, but survive inbox round-trips)
	drafts       []draftData
	draftsCursor int
}

// New builds a TUI model backed by a real IMAP/SMTP session. The returned
// model's session is the same IMAPSession the caller would obtain from
// NewWithSession, so the caller can still call Logout on it after Run().
//
// NewWithSession is preferred in production: pass an externally-built
// mail.Session (the real *mail.IMAPSession) so the caller can clean it up
// after the program exits.
//
//nolint:revive // unexported-return: returning *model is deliberate — callers use it via tea.Model and the type stays opaque.
func New(server string, insecure bool, cfg config.Config) *model {
	return NewWithSession(mail.NewIMAPSession(server, insecure), server, insecure, cfg)
}

// NewWithSession lets tests inject a fake Session behind the seam, and lets
// the caller (cmd/tui) retain a reference to the session so Logout can run
// after the Bubble Tea program returns.
//
//nolint:revive // unexported-return: see New.
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
		composeSubject: cs,
		viewport:       vp,
	}
}

func (m *model) Init() tea.Cmd {
	return textinput.Blink
}

// Logout closes the IMAP session synchronously. It is intended to be called
// by cmd/tui after the Bubble Tea program returns, so the IMAP LOGOUT is
// always sent instead of being dropped when the message loop shuts down. The
// call is bounded: a server that doesn't reply to LOGOUT within 5s is
// dropped.
//
// The method is exported even though the type isn't, because cmd/tui holds a
// *model value returned by the exported NewWithSession and can call exported
// methods on it.
func (m *model) Logout() error {
	m.cancel()
	if m.session == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return m.session.Logout(ctx)
}

func classifyGroup(t time.Time) dateGroup {
	now := time.Now()
	nowDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	tDate := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())

	diff := nowDate.Sub(tDate)
	switch {
	case diff < 24*time.Hour:
		return groupToday
	case diff < 48*time.Hour:
		return groupYesterday
	case diff < 7*24*time.Hour:
		return groupThisWeek
	default:
		return groupEarlier
	}
}

func groupLabel(g dateGroup) string {
	switch g {
	case groupToday:
		return "Today"
	case groupYesterday:
		return "Yesterday"
	case groupThisWeek:
		return "This Week"
	default:
		return "Earlier"
	}
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
			m.composeSubject, cmd = m.composeSubject.Update(msg)
			cmds = append(cmds, cmd)
		case 2:
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

func (m *model) advanceComposeField() {
	switch m.composeCursor {
	case 0:
		m.composeTo.Blur()
		m.composeSubject.Focus()
		m.composeCursor = 1
	case 1:
		m.composeSubject.Blur()
		m.composeCursor = 2
	case 2:
		m.composeTo.Focus()
		m.composeCursor = 0
	}
}

func (m *model) retreatComposeField() {
	switch m.composeCursor {
	case 0:
		m.composeCursor = 2
		m.composeTo.Blur()
	case 1:
		m.composeCursor = 0
		m.composeSubject.Blur()
		m.composeTo.Focus()
	case 2:
		m.composeCursor = 1
		m.composeSubject.Focus()
	}
}

// --- commands ---
//
// Each command performs one session call and returns a message carrying the
// result. None of them mutate the model: that happens in Update when the
// message is received, which keeps view state single-threaded.

func (m *model) login() tea.Cmd {
	user := m.username
	pass := m.password
	session := m.session
	ctx := m.ctx
	return func() tea.Msg {
		emails, err := session.Login(ctx, user, pass)
		if err != nil {
			return errMsg{err: err}
		}
		return loginSuccess{emails: emails}
	}
}

func (m *model) refreshInbox() tea.Cmd {
	session := m.session
	ctx := m.ctx
	return func() tea.Msg {
		emails, err := session.Refresh(ctx)
		if err != nil {
			return errMsg{err: err}
		}
		return inboxLoaded{emails: emails}
	}
}

func (m *model) fetchMessage(uid uint32) tea.Cmd {
	session := m.session
	ctx := m.ctx
	return func() tea.Msg {
		full, err := session.Fetch(ctx, uid)
		if err != nil {
			return errMsg{err: err}
		}
		return messageLoaded{email: full, uid: uid}
	}
}

func (m *model) sendMail() tea.Cmd {
	to := strings.TrimSpace(m.composeTo.Value())
	subject := strings.TrimSpace(m.composeSubject.Value())
	body := strings.TrimSpace(m.composeBody)
	session := m.session
	ctx := m.ctx
	out := mail.Outgoing{To: to, Subject: subject, Body: body}
	return func() tea.Msg {
		if err := session.Send(ctx, out); err != nil {
			return errMsg{err: err}
		}
		return sentMsg{}
	}
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

func (m *model) loadingView() string {
	return lipgloss.NewStyle().
		Width(m.width).
		Height(m.height).
		Align(lipgloss.Center, lipgloss.Center).
		Render(m.styles.Muted.Render(m.loading + "..."))
}

func (m *model) loginView() string {
	formW := m.width
	if formW < 40 {
		formW = 40
	}
	if formW > 60 {
		formW = 60
	}

	rule := m.styles.Muted.Render(strings.Repeat("\u2500", formW))

	title := lipgloss.JoinVertical(lipgloss.Center,
		m.styles.Primary.Bold(true).Render("Hooli Mail"),
		m.styles.Muted.Render(m.server),
	)

	inputBox := lipgloss.JoinVertical(lipgloss.Left,
		m.emailInput.View(),
		m.passwordInput.View(),
	)

	var hint string
	if m.emailInput.Focused() {
		hint = m.styles.Muted.Render("enter email  \u00b7  tab to switch")
	} else {
		hint = m.styles.Muted.Render("enter password  \u00b7  tab to switch  \u00b7  enter to sign in")
	}

	var errStr string
	if m.err != nil {
		errStr = m.styles.Error.Render(m.err.Error())
	}

	return lipgloss.NewStyle().
		Width(m.width).
		Height(m.height).
		Align(lipgloss.Center, lipgloss.Center).
		Render(
			lipgloss.JoinVertical(lipgloss.Center,
				title,
				"",
				rule,
				"",
				inputBox,
				"",
				rule,
				"",
				errStr,
				hint,
			),
		)
}

func (m *model) inboxView() string {
	contentW := m.width - 4
	if contentW < 40 {
		contentW = 40
	}

	dot := m.styles.Muted.Render(" \u00b7 ")
	statusLine := "  " +
		m.styles.StatusBold.Render("INBOX") +
		dot +
		m.styles.StatusAccent.Render(fmt.Sprintf("%d", m.total)) +
		dot +
		m.styles.Muted.Render("@"+m.loggedInUser)
	if n := len(m.drafts); n > 0 {
		noun := "draft"
		if n > 1 {
			noun = "drafts"
		}
		statusLine += dot + m.styles.Muted.Render(fmt.Sprintf("[%d %s]", n, noun))
	}

	rule := m.styles.Muted.Render(strings.Repeat("\u2500", max(0, contentW-4)))

	// Fixed chrome: statusLine + topRule + bottomRule + footer = 4 lines.
	// An error line (with leading blank) steals 2 more.
	availHeight := m.height - 4
	var errBlock string
	if m.err != nil {
		errBlock = m.styles.Error.Render(m.err.Error())
		availHeight -= 2
	}
	if availHeight < 1 {
		availHeight = 1
	}

	var middle string
	if len(m.emails) == 0 {
		empty := lipgloss.JoinVertical(lipgloss.Center,
			"",
			m.styles.Muted.Render("No messages yet"),
			"",
			m.styles.Muted.Render("c to compose  \u00b7  r to refresh"),
		)
		middle = padToHeight(empty, availHeight)
	} else {
		bodyW := contentW - 4
		middle = m.renderEmailList(bodyW, availHeight)
	}

	rows := []string{statusLine, rule, middle}
	if m.err != nil {
		rows = append(rows, "", errBlock)
	}
	rows = append(rows, rule, m.renderInboxFooter())

	return lipgloss.NewStyle().Width(m.width).Render(
		lipgloss.JoinVertical(lipgloss.Left, rows...),
	)
}

func (m *model) renderInboxFooter() string {
	k := func(s string) string { return m.styles.StatusAccent.Render("[" + s + "]") }
	v := func(s string) string { return m.styles.Footer.Render(" " + s) }
	sep := m.styles.Muted.Render("  ")
	return "  " +
		k("j/k") + v("navigate") + sep +
		k("enter") + v("read") + sep +
		k("c") + v("compose") + sep +
		k("D") + v("drafts") + sep +
		k("r") + v("refresh") + sep +
		k("q") + v("quit")
}

func (m *model) renderMessageFooter() string {
	k := func(s string) string { return m.styles.StatusAccent.Render("[" + s + "]") }
	v := func(s string) string { return m.styles.Footer.Render(" " + s) }
	sep := m.styles.Muted.Render("  ")
	return "  " + k("esc") + v("back") + sep + k("\u2191/\u2193") + v("scroll")
}

func (m *model) renderComposeFooter() string {
	k := func(s string) string { return m.styles.StatusAccent.Render("[" + s + "]") }
	v := func(s string) string { return m.styles.Footer.Render(" " + s) }
	sep := m.styles.Muted.Render("  ")
	return "  " + k("tab") + v("next") + sep + k("^S") + v("send") + sep + k("esc") + v("cancel")
}

func (m *model) renderDraftsFooter() string {
	k := func(s string) string { return m.styles.StatusAccent.Render("[" + s + "]") }
	v := func(s string) string { return m.styles.Footer.Render(" " + s) }
	sep := m.styles.Muted.Render("  ")
	return "  " + k("j/k") + v("navigate") + sep + k("enter") + v("resume") + sep + k("d") + v("delete") + sep + k("esc") + v("back")
}

func (m *model) renderDraftConfirm() string {
	k := func(s string) string { return m.styles.StatusAccent.Render("[" + s + "]") }

	var subjectPreview string
	if s := strings.TrimSpace(m.composeSubject.Value()); s != "" {
		subjectPreview = m.styles.Muted.Render("  " + truncate(s, 40))
	}

	body := lipgloss.JoinVertical(lipgloss.Left,
		m.styles.Primary.Bold(true).Render("Abandon draft?"),
		subjectPreview,
		"",
		k("s")+" "+m.styles.Secondary.Render("save for later"),
		k("d")+" "+m.styles.Secondary.Render("discard"),
		k("k")+" "+m.styles.Secondary.Render("keep writing"),
	)

	modal := m.styles.ModalBox.Render(body)

	return lipgloss.NewStyle().
		Width(m.width).
		Height(m.height).
		Align(lipgloss.Center, lipgloss.Center).
		Render(modal)
}

// truncate shortens s to at most n runes, appending … if needed.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "\u2026"
}

// padToHeight pads content with blank lines so it occupies exactly height lines.
func padToHeight(content string, height int) string {
	lines := strings.Split(content, "\n")
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// windowLines slices a list of display lines to maxLines, keeping the cursor
// line roughly centered when scrolling is needed.
func windowLines(lines []string, cursorLine, maxLines int) []string {
	if len(lines) <= maxLines {
		out := lines
		for len(out) < maxLines {
			out = append(out, "")
		}
		return out
	}

	half := maxLines / 2
	start := 0
	switch {
	case cursorLine < half:
		start = 0
	case cursorLine >= len(lines)-half:
		start = len(lines) - maxLines
	default:
		start = cursorLine - half
	}
	if start < 0 {
		start = 0
	}

	end := start + maxLines
	if end > len(lines) {
		end = len(lines)
	}
	return lines[start:end]
}

func (m *model) renderEmailList(bodyW, maxLines int) string {
	groups := m.buildGroups()
	var lines []string
	cursorLine := 0

	for _, g := range groups {
		label := "  " + g.label + "  "
		ruleLen := max(0, bodyW-len(label))
		groupLine := m.styles.Group.Render(label) + m.styles.Muted.Render(strings.Repeat("\u2500", ruleLen))
		lines = append(lines, groupLine)

		for _, idx := range g.indices {
			if idx == m.cursor {
				cursorLine = len(lines)
			}
			lines = append(lines, m.renderRow(m.emails[idx], idx == m.cursor, bodyW))
		}
	}

	visible := windowLines(lines, cursorLine, maxLines)
	return lipgloss.JoinVertical(lipgloss.Left, visible...)
}

type groupInfo struct {
	label   string
	indices []int
}

func (m *model) buildGroups() []groupInfo {
	const groupNone dateGroup = -1
	currentGroup := groupNone
	var groups []groupInfo
	var current groupInfo

	for i, e := range m.emails {
		g := classifyGroup(e.Date)
		if g != currentGroup {
			if current.label != "" {
				groups = append(groups, current)
			}
			current = groupInfo{
				label:   groupLabel(g),
				indices: []int{i},
			}
			currentGroup = g
		} else {
			current.indices = append(current.indices, i)
		}
	}
	if current.label != "" {
		groups = append(groups, current)
	}

	return groups
}

func (m *model) renderRow(email mail.Summary, selected bool, width int) string {
	fromW := 20
	dateW := 10
	subjW := width - 1 - 2 - 1 - fromW - 1 - dateW - 2
	if subjW < 10 {
		subjW = 10
	}

	sealStr := "  "
	if !email.Seen {
		sealStr = m.styles.Seal.Render("\u2588\u2588")
	}

	from := email.From
	if from == "" {
		from = "(unknown)"
	}
	if len([]rune(from)) > fromW {
		from = string([]rune(from)[:fromW-1]) + "\u2026"
	}

	subject := email.Subject
	if subject == "" {
		subject = "(no subject)"
	}
	if len([]rune(subject)) > subjW {
		subject = string([]rune(subject)[:subjW-1]) + "\u2026"
	}

	age := time.Since(email.Date)
	var dateStr string
	switch {
	case age < 24*time.Hour:
		dateStr = email.Date.Format("15:04")
	case age < 7*24*time.Hour:
		dateStr = email.Date.Format("Mon")
	default:
		dateStr = email.Date.Format("Jan 02")
	}

	fromPadded := fmt.Sprintf("%-*s", fromW, from)
	subjPadded := fmt.Sprintf("%-*s", subjW, subject)

	var content string
	if !email.Seen {
		content = sealStr + " " +
			m.styles.Primary.Bold(true).Render(fromPadded) + " " +
			m.styles.Secondary.Bold(true).Render(subjPadded) + " " +
			m.styles.Muted.Render(dateStr)
	} else {
		content = sealStr + " " +
			m.styles.Secondary.Render(fromPadded) + " " +
			m.styles.Muted.Render(subjPadded) + " " +
			m.styles.Muted.Render(dateStr)
	}

	if selected {
		return m.styles.CursorBar.Render("\u258c") + m.styles.Cursor.Render(content)
	}
	return " " + content
}

func (m *model) messageView() string {
	if m.viewing == nil {
		return ""
	}

	contentW := m.width - 4

	back := m.styles.Secondary.Render("\u2190  ") + m.styles.Muted.Render("Inbox")

	subject := m.styles.Subject.Render(m.viewing.Subject)

	metaFrom := fmt.Sprintf("%s %s", m.styles.MetaKey.Render("From:"), m.styles.MetaVal.Render(m.viewing.From))
	metaTo := fmt.Sprintf("%s %s", m.styles.MetaKey.Render("To:"), m.styles.MetaVal.Render(strings.Join(m.viewing.To, ", ")))
	metaDate := fmt.Sprintf("%s %s", m.styles.MetaKey.Render("Date:"), m.styles.MetaVal.Render(m.viewing.Date.Format("Mon 2 Jan 2006 at 15:04")))

	bodyW := contentW - 4
	body := m.viewing.Body
	renderer, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(bodyW),
	)
	if err == nil {
		rendered, rerr := renderer.Render(body)
		if rerr == nil {
			body = rendered
		}
	}
	m.viewport.SetContent(body)
	m.viewport.Width = bodyW
	// Header is 8 lines (back + blank + subject + blank + from + to + date + rule).
	// Footer is 2 lines (rule + footer). Viewport fills the rest.
	m.viewport.Height = m.height - 10

	rule := m.styles.Muted.Render(strings.Repeat("\u2500", max(0, contentW-4)))

	return lipgloss.NewStyle().Width(m.width).Render(
		lipgloss.JoinVertical(lipgloss.Left,
			"  "+back,
			"",
			"  "+subject,
			"",
			"  "+metaFrom,
			"  "+metaTo,
			"  "+metaDate,
			rule,
			m.viewport.View(),
			rule,
			m.renderMessageFooter(),
		),
	)
}

func (m *model) composeView() string {
	contentW := m.width - 4
	if contentW > 72 {
		contentW = 72
	}

	header := m.styles.Primary.Bold(true).Render("New Message")
	rule := m.styles.Muted.Render(strings.Repeat("\u2500", contentW))

	activeLabel := func(label string, cursor int, field int) string {
		if cursor == field {
			return m.styles.Secondary.Width(6).Align(lipgloss.Right).Render(label)
		}
		return m.styles.ComposeLabel.Render(label)
	}

	toField := activeLabel("To:", m.composeCursor, 0) + " " + m.composeTo.View()
	subjField := activeLabel("Subj:", m.composeCursor, 1) + " " + m.composeSubject.View()

	bodyContent := m.composeBody
	if m.composeCursor == 2 {
		bodyContent += m.styles.Seal.Render("\u2588")
	} else if bodyContent == "" {
		bodyContent = m.styles.Muted.Render("Write your message...")
	}
	bodyField := activeLabel("Body:", m.composeCursor, 2) + " " + bodyContent

	return lipgloss.NewStyle().Width(m.width).Height(m.height).Render(
		lipgloss.JoinVertical(lipgloss.Left,
			"  "+header,
			rule,
			"",
			"  "+toField,
			"  "+subjField,
			"",
			"  "+bodyField,
			"",
			rule,
			m.renderComposeFooter(),
		),
	)
}

func (m *model) draftsView() string {
	contentW := m.width - 4
	if contentW < 40 {
		contentW = 40
	}

	header := m.styles.Primary.Bold(true).Render("Drafts")
	rule := m.styles.Muted.Render(strings.Repeat("\u2500", max(0, contentW-4)))

	// Fixed chrome: header + topRule + bottomRule + footer = 4 lines.
	availHeight := m.height - 4
	if availHeight < 1 {
		availHeight = 1
	}

	bodyW := contentW - 4
	fromW := 24
	subjW := bodyW - fromW - 3
	if subjW < 10 {
		subjW = 10
	}

	var lines []string
	for i, d := range m.drafts {
		selected := i == m.draftsCursor

		to := d.To
		if to == "" {
			to = "(no recipient)"
		}
		subject := d.Subject
		if subject == "" {
			subject = "(no subject)"
		}
		if len([]rune(to)) > fromW {
			to = string([]rune(to)[:fromW-1]) + "\u2026"
		}
		if len([]rune(subject)) > subjW {
			subject = string([]rune(subject)[:subjW-1]) + "\u2026"
		}

		fromPadded := fmt.Sprintf("%-*s", fromW, to)
		subjPadded := fmt.Sprintf("%-*s", subjW, subject)

		content := m.styles.Secondary.Render(fromPadded) + " " +
			m.styles.Muted.Render(subjPadded)

		if selected {
			lines = append(lines, m.styles.CursorBar.Render("\u258c")+m.styles.Cursor.Render(content))
		} else {
			lines = append(lines, " "+content)
		}
	}

	visible := windowLines(lines, m.draftsCursor, availHeight)

	return lipgloss.NewStyle().Width(m.width).Render(
		lipgloss.JoinVertical(lipgloss.Left,
			"  "+header,
			rule,
			lipgloss.JoinVertical(lipgloss.Left, visible...),
			rule,
			m.renderDraftsFooter(),
		),
	)
}

// --- messages ---

// draftData holds a compose session that the user chose to save for later.
// It lives only in memory for the lifetime of the process.
type draftData struct {
	To      string
	Subject string
	Body    string
}

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
