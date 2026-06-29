package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// --- message reading workflow: rendering and fetch command ---

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

func (m *model) renderMessageFooter() string {
	k := func(s string) string { return m.styles.StatusAccent.Render("[" + s + "]") }
	v := func(s string) string { return m.styles.Footer.Render(" " + s) }
	sep := m.styles.Muted.Render("  ")
	return "  " + k("esc") + v("back") + sep + k("\u2191/\u2193") + v("scroll")
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
