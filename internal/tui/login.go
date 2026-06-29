package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// --- login workflow: rendering and command ---

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

// loadingView is shared across workflows — any state can show a loading overlay.
func (m *model) loadingView() string {
	return lipgloss.NewStyle().
		Width(m.width).
		Height(m.height).
		Align(lipgloss.Center, lipgloss.Center).
		Render(m.styles.Muted.Render(m.loading + "..."))
}
