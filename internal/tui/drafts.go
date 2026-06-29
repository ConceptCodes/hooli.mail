package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// --- drafts workflow: rendering ---

func (m *model) draftsView() string {
	contentW := m.width - 4
	if contentW < 40 {
		contentW = 40
	}

	header := m.styles.Primary.Bold(true).Render("Drafts")
	rule := m.styles.Muted.Render(strings.Repeat("\u2500", max(0, contentW-4)))

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

func (m *model) renderDraftsFooter() string {
	k := func(s string) string { return m.styles.StatusAccent.Render("[" + s + "]") }
	v := func(s string) string { return m.styles.Footer.Render(" " + s) }
	sep := m.styles.Muted.Render("  ")
	return "  " + k("j/k") + v("navigate") + sep + k("enter") + v("resume") + sep + k("d") + v("delete") + sep + k("esc") + v("back")
}
