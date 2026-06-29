package tui

import (
	"strings"

	"hooli.mail/server/internal/tui/mail"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// --- compose workflow: rendering, field navigation, send command ---

// draftData holds a compose session that the user chose to save for later.
// It lives only in memory for the lifetime of the process.
type draftData struct {
	To      string
	Cc      string
	Bcc     string
	Subject string
	Body    string
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
	ccField := activeLabel("Cc:", m.composeCursor, 1) + " " + m.composeCc.View()
	bccField := activeLabel("Bcc:", m.composeCursor, 2) + " " + m.composeBcc.View()
	subjField := activeLabel("Subj:", m.composeCursor, 3) + " " + m.composeSubject.View()

	bodyContent := m.composeBody
	if m.composeCursor == 4 {
		bodyContent += m.styles.Seal.Render("\u2588")
	} else if bodyContent == "" {
		bodyContent = m.styles.Muted.Render("Write your message...")
	}
	bodyField := activeLabel("Body:", m.composeCursor, 4) + " " + bodyContent

	return lipgloss.NewStyle().Width(m.width).Height(m.height).Render(
		lipgloss.JoinVertical(lipgloss.Left,
			"  "+header,
			rule,
			"",
			"  "+toField,
			"  "+ccField,
			"  "+bccField,
			"  "+subjField,
			"",
			"  "+bodyField,
			"",
			rule,
			m.renderComposeFooter(),
		),
	)
}

func (m *model) renderComposeFooter() string {
	k := func(s string) string { return m.styles.StatusAccent.Render("[" + s + "]") }
	v := func(s string) string { return m.styles.Footer.Render(" " + s) }
	sep := m.styles.Muted.Render("  ")
	return "  " + k("tab") + v("next") + sep + k("^S") + v("send") + sep + k("esc") + v("cancel")
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

func (m *model) advanceComposeField() {
	switch m.composeCursor {
	case 0:
		m.composeTo.Blur()
		m.composeCc.Focus()
		m.composeCursor = 1
	case 1:
		m.composeCc.Blur()
		m.composeBcc.Focus()
		m.composeCursor = 2
	case 2:
		m.composeBcc.Blur()
		m.composeCursor = 3
	case 3:
		m.composeCursor = 4
	case 4:
		m.composeTo.Focus()
		m.composeCursor = 0
	}
}

func (m *model) retreatComposeField() {
	switch m.composeCursor {
	case 0:
		m.composeCursor = 4
		m.composeTo.Blur()
	case 1:
		m.composeCursor = 0
		m.composeCc.Blur()
		m.composeTo.Focus()
	case 2:
		m.composeCursor = 1
		m.composeBcc.Blur()
		m.composeCc.Focus()
	case 3:
		m.composeCursor = 2
		m.composeSubject.Blur()
		m.composeBcc.Focus()
	case 4:
		m.composeCursor = 3
		m.composeSubject.Focus()
	}
}

func (m *model) sendMail() tea.Cmd {
	to := strings.TrimSpace(m.composeTo.Value())
	cc := strings.TrimSpace(m.composeCc.Value())
	bcc := strings.TrimSpace(m.composeBcc.Value())
	subject := strings.TrimSpace(m.composeSubject.Value())
	body := strings.TrimSpace(m.composeBody)
	session := m.session
	ctx := m.ctx
	out := mail.Outgoing{To: to, Cc: cc, Bcc: bcc, Subject: subject, Body: body}
	return func() tea.Msg {
		if err := session.Send(ctx, out); err != nil {
			return errMsg{err: err}
		}
		return sentMsg{}
	}
}
