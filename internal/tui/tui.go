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
	emails  []mail.Summary
	cursor  int
	total   int
	offset  int

	viewing *mail.Full
	viewport viewport.Model

	composeTo      textinput.Model
	composeSubject textinput.Model
	composeBody    string
	composeCursor  int
	composeBuf     []string

	err     error
	loading string
}

// New builds a TUI model backed by a real IMAP/SMTP session.
func New(server string, insecure bool, cfg config.Config) *model {
	return newWithSession(mail.NewIMAPSession(server, insecure), server, insecure, cfg)
}

// newWithSession lets tests inject a fake Session behind the seam.
func newWithSession(session mail.Session, server string, insecure bool, cfg config.Config) *model {
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

	return &model{
		state:          loginView,
		styles:         s,
		server:         server,
		insecure:       insecure,
		session:        session,
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
		m.viewport.Height = msg.Height - 8

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "ctrl+q":
			if m.state == inboxView || m.state == loginView {
				return m, tea.Quit
			}
		case "q":
			if m.state == inboxView && len(m.emails) == 0 {
				return m, tea.Quit
			}
		case "escape":
			switch m.state {
			case messageView:
				m.state = inboxView
				m.viewing = nil
				return m, nil
			case composeView:
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
				m.ensureVisible()
			}
		case "down", "j":
			if m.state == inboxView && m.cursor < len(m.emails)-1 {
				m.cursor++
				m.ensureVisible()
			}
		case "g":
			if m.state == inboxView {
				m.cursor = 0
				m.offset = 0
			}
		case "G":
			if m.state == inboxView {
				m.cursor = len(m.emails) - 1
				m.ensureVisible()
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
				m.composeBuf = nil
				m.composeCursor = 0
				m.composeTo.Focus()
				m.composeTo.Prompt = ""
				m.composeSubject.Prompt = ""
				m.state = composeView
				return m, nil
			}
		case "ctrl+s":
			if m.state == composeView && m.composeCursor == 2 {
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
		m.offset = 0
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

func (m *model) ensureVisible() {
	rows := m.height - 4
	half := rows / 2
	if m.cursor < m.offset {
		m.offset = m.cursor
	} else if m.cursor >= m.offset+rows {
		m.offset = m.cursor - rows + 1
	}
	if m.cursor-m.offset < half && m.offset > 0 {
		m.offset = max(0, m.cursor-half)
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
	return func() tea.Msg {
		emails, err := session.Login(context.Background(), user, pass)
		if err != nil {
			return errMsg{err: err}
		}
		return loginSuccess{emails: emails}
	}
}

func (m *model) refreshInbox() tea.Cmd {
	session := m.session
	return func() tea.Msg {
		emails, err := session.Refresh(context.Background())
		if err != nil {
			return errMsg{err: err}
		}
		return inboxLoaded{emails: emails}
	}
}

func (m *model) fetchMessage(uid uint32) tea.Cmd {
	session := m.session
	return func() tea.Msg {
		full, err := session.Fetch(context.Background(), uid)
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
	out := mail.Outgoing{To: to, Subject: subject, Body: body}
	return func() tea.Msg {
		if err := session.Send(context.Background(), out); err != nil {
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
	contentWidth := m.width
	if contentWidth < 40 {
		contentWidth = 40
	}
	if contentWidth > 80 {
		contentWidth = 80
	}

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
		hint = m.styles.Muted.Render("enter email · tab to password")
	} else {
		hint = m.styles.Muted.Render("enter password · tab to email · enter to login")
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
				"",
				title,
				"",
				"",
				inputBox,
				"",
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

	statusLine := fmt.Sprintf("  %s  %s  %s",
		m.styles.StatusBold.Render("INBOX"),
		m.styles.StatusAccent.Render(fmt.Sprintf("%d", m.total)),
		m.styles.Muted.Render(fmt.Sprintf("@%s", m.loggedInUser)),
	)

	var rows []string
	rows = append(rows, statusLine)
	rows = append(rows, m.styles.Muted.Render(strings.Repeat("\u2500", max(0, contentW-4))))

	if len(m.emails) == 0 {
		empty := lipgloss.JoinVertical(lipgloss.Center,
			"",
			m.styles.Muted.Render("No messages yet"),
			"",
			m.styles.Muted.Render("c to compose  r to refresh"),
		)
		rows = append(rows, empty)
	} else {
		bodyW := contentW - 4
		rows = append(rows, m.renderEmailList(bodyW))
	}

	if m.err != nil {
		rows = append(rows, "")
		rows = append(rows, m.styles.Error.Render(m.err.Error()))
	}

	footer := m.styles.Footer.Render(formatInboxFooter())
	rows = append(rows, footer)

	return lipgloss.NewStyle().Width(m.width).Height(m.height).Render(
		lipgloss.JoinVertical(lipgloss.Left, rows...),
	)
}

func formatInboxFooter() string {
	return "\u2022 j/k navigate  \u2022 enter read  \u2022 c compose  \u2022 r refresh  \u2022 q quit"
}

func (m *model) renderEmailList(bodyW int) string {
	groups := m.buildGroups()
	var lines []string

	for _, g := range groups {
		lines = append(lines, m.styles.Group.Render("  "+g.label))
		lines = append(lines, "")

		for _, idx := range g.indices {
			email := m.emails[idx]
			selected := idx == m.cursor
			lines = append(lines, m.renderRow(email, selected, bodyW))
		}
	}

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

type groupInfo struct {
	label   string
	indices []int
}

func (m *model) buildGroups() []groupInfo {
	var currentGroup dateGroup
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
	subjW := width - 2 - fromW - dateW - 4
	if subjW < 10 {
		subjW = 10
	}

	seal := "  "
	if !email.Seen {
		seal = m.styles.Seal.Render("\u2588\u2588")
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

	var line string
	if !email.Seen {
		fromPadded := fmt.Sprintf("%-*s", fromW, from)
		subjPadded := fmt.Sprintf("%-*s", subjW, subject)
		line = seal + " " +
			m.styles.Primary.Bold(true).Render(fromPadded) + " " +
			m.styles.Secondary.Bold(true).Render(subjPadded) + " " +
			m.styles.Muted.Render(dateStr)
	} else {
		fromPadded := fmt.Sprintf("%-*s", fromW, from)
		subjPadded := fmt.Sprintf("%-*s", subjW, subject)
		line = seal + " " +
			m.styles.Primary.Render(fromPadded) + " " +
			m.styles.Secondary.Render(subjPadded) + " " +
			m.styles.Muted.Render(dateStr)
	}

	if selected {
		line = m.styles.Cursor.Render(line)
	}

	return line
}

func (m *model) messageView() string {
	if m.viewing == nil {
		return ""
	}

	contentW := m.width - 4

	back := m.styles.Secondary.Render("\u2190  ") + m.styles.Muted.Render("Inbox")

	subject := m.styles.Subject.Render(m.viewing.Subject)

	metaFrom := fmt.Sprintf("%s %s", m.styles.MetaLabel.Render("From:"), m.styles.Secondary.Render(m.viewing.From))
	metaTo := fmt.Sprintf("%s %s", m.styles.MetaLabel.Render("To:"), m.styles.Secondary.Render(strings.Join(m.viewing.To, ", ")))
	metaDate := fmt.Sprintf("%s %s", m.styles.MetaLabel.Render("Date:"), m.styles.Secondary.Render(m.viewing.Date.Format("Mon 2 Jan 2006 at 15:04")))

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
	m.viewport.Height = m.height - 12

	footer := m.styles.Footer.Render(formatMessageFooter())

	return lipgloss.NewStyle().Width(m.width).Height(m.height).Render(
		lipgloss.JoinVertical(lipgloss.Left,
			"  "+back,
			"",
			subject,
			"",
			"  "+metaFrom,
			"  "+metaTo,
			"  "+metaDate,
			m.styles.Muted.Render(strings.Repeat("\u2500", max(0, contentW-4))),
			m.viewport.View(),
			footer,
		),
	)
}

func formatMessageFooter() string {
	return "\u2022 esc back  \u2022 \u2191/\u2193 scroll"
}

func (m *model) composeView() string {
	contentW := m.width - 4
	if contentW > 72 {
		contentW = 72
	}
	fieldW := contentW - 10
	if fieldW < 30 {
		fieldW = 30
	}

	header := m.styles.Primary.Bold(true).Render("New Message")

	toField := m.styles.ComposeLabel.Render("To:") + " " + m.composeTo.View()
	subjField := m.styles.ComposeLabel.Render("Subj:") + " " + m.composeSubject.View()

	bodyContent := m.composeBody
	if bodyContent == "" {
		bodyContent = m.styles.Muted.Render("Write your message...")
	}
	bodyField := m.styles.ComposeLabel.Render("Body:") + " " + bodyContent

	footer := m.styles.Footer.Render(formatComposeFooter())

	return lipgloss.JoinVertical(lipgloss.Left,
		"  "+header,
		"",
		"  "+toField,
		"  "+subjField,
		"  "+bodyField,
		"",
		footer,
	)
}

func formatComposeFooter() string {
	return "\u2022 tab next  \u2022 enter newline  \u2022 ^S send  \u2022 esc cancel"
}

// --- messages ---

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
