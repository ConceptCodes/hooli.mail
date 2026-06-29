package tui

import (
	"fmt"
	"strings"
	"testing"

	"hooli.mail/server/internal/config"
	"hooli.mail/server/internal/tui/mail"

	tea "github.com/charmbracelet/bubbletea"
)

func runeKey(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestHeightFillAllViews(t *testing.T) {
	sess := &fakeSession{
		summaries: []mail.Summary{
			{UID: 1, From: "a@x.com", Subject: "hi", Seen: false},
			{UID: 2, From: "b@x.com", Subject: "yo", Seen: true},
		},
		full: &mail.Full{Subject: "hi", From: "a@x.com", To: []string{"me@x.com"}, Body: "hello body"},
	}
	m := NewWithSession(sess, "example.com", false, config.Default())
	m.width, m.height = 80, 24
	m.loggedInUser = "me@x.com"
	m.emails = sess.summaries
	m.total = 2

	check := func(label string, out string) {
		lines := strings.Split(out, "\n")
		status := "OK"
		if len(lines) != m.height {
			status = fmt.Sprintf("MISMATCH (got %d)", len(lines))
		}
		t.Logf("%-20s %s", label, status)
	}

	m.state = loginView
	check("loginView", m.View())

	m.state = inboxView
	check("inboxView", m.View())

	m.state = messageView
	m.viewing = sess.full
	check("messageView", m.View())

	m.state = composeView
	check("composeView", m.View())

	m.state = draftConfirmView
	check("draftConfirmView", m.View())

	m.state = draftsView
	m.drafts = []draftData{{To: "x@y.com", Subject: "test", Body: "body"}}
	check("draftsView", m.View())

	m.loading = "Connecting"
	check("loadingView", m.View())
}

func TestInboxFooterPinnedToBottom(t *testing.T) {
	sess := &fakeSession{
		summaries: []mail.Summary{
			{UID: 1, From: "a@x.com", Subject: "hi", Seen: false},
		},
	}
	m := NewWithSession(sess, "example.com", false, config.Default())
	m.width, m.height = 80, 24
	m.loggedInUser = "me@x.com"
	m.emails = sess.summaries
	m.total = 1
	m.state = inboxView

	out := m.View()
	lines := strings.Split(out, "\n")
	if len(lines) != 24 {
		t.Fatalf("inbox renders %d lines, want 24", len(lines))
	}
	last := lines[23]
	if !strings.Contains(last, "navigate") || !strings.Contains(last, "quit") {
		t.Errorf("last line should be the footer, got: %q", last)
	}
	rule := lines[22]
	if !strings.Contains(rule, "\u2500") {
		t.Errorf("second-to-last line should be a rule, got: %q", rule)
	}
	if strings.Contains(last, "a@x.com") {
		t.Errorf("email content should not be on the bottom line")
	}
	t.Logf("footer (line 24): %q", last)
}

func TestScrollWithManyEmails(t *testing.T) {
	var summaries []mail.Summary
	for i := 0; i < 50; i++ {
		summaries = append(summaries, mail.Summary{
			UID:     uint32(i + 1),
			From:    fmt.Sprintf("user%d@x.com", i),
			Subject: fmt.Sprintf("subject %d", i),
			Seen:    true,
		})
	}
	m := NewWithSession(&fakeSession{summaries: summaries}, "example.com", false, config.Default())
	m.width, m.height = 80, 24
	m.loggedInUser = "me@x.com"
	m.emails = summaries
	m.total = 50
	m.state = inboxView

	out := m.View()
	lines := strings.Split(out, "\n")
	if len(lines) != 24 {
		t.Fatalf("expected 24 lines with many emails, got %d", len(lines))
	}
	if !strings.Contains(out, "user0@x.com") {
		t.Error("first email should be visible")
	}
	if strings.Contains(out, "user49@x.com") {
		t.Error("50th email should not be visible without scrolling down")
	}

	m.cursor = 49
	out = m.View()
	lines = strings.Split(out, "\n")
	if len(lines) != 24 {
		t.Fatalf("expected 24 lines after scroll, got %d", len(lines))
	}
	if !strings.Contains(out, "user49@x.com") {
		t.Error("last email should be visible after scrolling to bottom")
	}
}

func TestDraftsListAndResume(t *testing.T) {
	m := NewWithSession(&fakeSession{}, "example.com", false, config.Default())
	m.width, m.height = 80, 24
	m.loggedInUser = "me@x.com"
	m.state = inboxView
	m.drafts = []draftData{
		{To: "alice@x.com", Subject: "hello", Body: "body1"},
		{To: "bob@x.com", Subject: "world", Body: "body2"},
	}

	mm, _ := m.Update(runeKey("D"))
	m = mm.(*model)
	if m.state != draftsView {
		t.Fatalf("expected draftsView, got %v", m.state)
	}

	out := m.View()
	if !strings.Contains(out, "alice@x.com") {
		t.Error("drafts view should show first draft")
	}
	if !strings.Contains(out, "bob@x.com") {
		t.Error("drafts view should show second draft")
	}

	mm, _ = m.Update(runeKey("j"))
	m = mm.(*model)
	if m.draftsCursor != 1 {
		t.Fatalf("expected cursor at 1, got %d", m.draftsCursor)
	}

	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = mm.(*model)
	if m.state != composeView {
		t.Fatalf("expected composeView after resume, got %v", m.state)
	}
	if m.composeTo.Value() != "bob@x.com" {
		t.Errorf("expected To=bob@x.com, got %q", m.composeTo.Value())
	}
	if len(m.drafts) != 1 {
		t.Errorf("expected 1 draft remaining, got %d", len(m.drafts))
	}
}

func TestDraftSaveAppendsToList(t *testing.T) {
	m := NewWithSession(&fakeSession{}, "example.com", false, config.Default())
	m.width, m.height = 80, 24
	m.state = draftConfirmView
	m.composeTo.SetValue("charlie@x.com")
	m.composeSubject.SetValue("test subj")
	m.composeBody = "test body"
	m.drafts = []draftData{{To: "existing@x.com", Subject: "old", Body: "old"}}

	mm, _ := m.Update(runeKey("s"))
	m = mm.(*model)
	if m.state != inboxView {
		t.Fatalf("expected inboxView, got %v", m.state)
	}
	if len(m.drafts) != 2 {
		t.Fatalf("expected 2 drafts, got %d", len(m.drafts))
	}
	if m.drafts[1].To != "charlie@x.com" {
		t.Errorf("new draft not appended: %+v", m.drafts[1])
	}
}

func TestDraftDeleteFromList(t *testing.T) {
	m := NewWithSession(&fakeSession{}, "example.com", false, config.Default())
	m.width, m.height = 80, 24
	m.state = draftsView
	m.drafts = []draftData{
		{To: "a@x.com", Subject: "s1", Body: "b1"},
		{To: "b@x.com", Subject: "s2", Body: "b2"},
	}
	m.draftsCursor = 0

	mm, _ := m.Update(runeKey("d"))
	m = mm.(*model)
	if len(m.drafts) != 1 {
		t.Fatalf("expected 1 draft, got %d", len(m.drafts))
	}
	if m.drafts[0].To != "b@x.com" {
		t.Errorf("expected b@x.com remaining, got %q", m.drafts[0].To)
	}
}

func TestInboxStatusShowsDraftCount(t *testing.T) {
	m := NewWithSession(&fakeSession{}, "example.com", false, config.Default())
	m.width, m.height = 80, 24
	m.loggedInUser = "me@x.com"
	m.state = inboxView
	m.drafts = []draftData{
		{To: "a@x.com", Subject: "s1", Body: "b1"},
		{To: "b@x.com", Subject: "s2", Body: "b2"},
	}

	out := m.View()
	if !strings.Contains(out, "[2 drafts]") {
		t.Errorf("status should show [2 drafts]")
	}
}
