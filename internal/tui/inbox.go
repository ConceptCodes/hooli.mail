package tui

import (
	"fmt"
	"strings"
	"time"

	"hooli.mail/server/internal/tui/mail"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// --- inbox workflow: date grouping, rendering, and refresh command ---

type dateGroup int

const (
	groupToday dateGroup = iota
	groupYesterday
	groupThisWeek
	groupEarlier
)

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
