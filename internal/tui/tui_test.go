package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"hooli.mail/server/internal/config"
	"hooli.mail/server/internal/tui/mail"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeSession is the test adapter behind the seam: it returns canned data so
// the reducer and view logic can run without a mail server.
type fakeSession struct {
	summaries []mail.Summary
	full      *mail.Full
	sent      *mail.Outgoing
}

func (f *fakeSession) Login(context.Context, string, string) ([]mail.Summary, error) {
	return f.summaries, nil
}
func (f *fakeSession) Refresh(context.Context) ([]mail.Summary, error) {
	return f.summaries, nil
}
func (f *fakeSession) Fetch(context.Context, uint32) (*mail.Full, error) { return f.full, nil }
func (f *fakeSession) Send(_ context.Context, out mail.Outgoing) error   { f.sent = &out; return nil }
func (f *fakeSession) Logout(context.Context) error                      { return nil }

func newTestModel(t *testing.T, sess mail.Session) *model {
	t.Helper()
	m := NewWithSession(sess, "example.com", false, config.Default())
	m.width, m.height = 80, 24
	return m
}

func TestClassifyGroup(t *testing.T) {
	now := time.Now()
	cases := []struct {
		when time.Time
		want dateGroup
	}{
		{now, groupToday},
		{now.AddDate(0, 0, -1), groupYesterday},
		{now.AddDate(0, 0, -3), groupThisWeek},
		{now.AddDate(0, 0, -20), groupEarlier},
	}
	for _, c := range cases {
		if got := classifyGroup(c.when); got != c.want {
			t.Errorf("classifyGroup(%s) = %v, want %v", c.when, got, c.want)
		}
	}
}

func TestBuildGroupsBucketsByDate(t *testing.T) {
	m := newTestModel(t, &fakeSession{})
	now := time.Now()
	// Anchor to noon so that subtracting 1h never crosses a calendar day
	// boundary (which would cause "Today" to shrink to 1 email near midnight).
	noon := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	m.emails = []mail.Summary{
		{UID: 1, Date: noon},
		{UID: 2, Date: noon.Add(-1 * time.Hour)},
		{UID: 3, Date: noon.AddDate(0, 0, -1)},
		{UID: 4, Date: noon.AddDate(0, 0, -20)},
	}
	groups := m.buildGroups()
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3", len(groups))
	}
	if groups[0].label != "Today" || len(groups[0].indices) != 2 {
		t.Errorf("first group = %+v", groups[0])
	}
	if groups[2].label != "Earlier" {
		t.Errorf("last group = %+v", groups[2])
	}
}

func TestRenderRowWaxSealOnlyWhenUnseen(t *testing.T) {
	m := newTestModel(t, &fakeSession{})
	m.emails = []mail.Summary{
		{UID: 1, From: "alice@x.com", Subject: "hi", Seen: false},
		{UID: 2, From: "bob@x.com", Subject: "yo", Seen: true},
	}
	unseen := m.renderRow(m.emails[0], false, 80)
	seen := m.renderRow(m.emails[1], false, 80)
	if !strings.Contains(unseen, "\u2588\u2588") {
		t.Error("unseen row should show the wax seal")
	}
	if strings.Contains(seen, "\u2588\u2588") {
		t.Error("seen row should not show the wax seal")
	}
}

func TestRenderRowTruncatesLongFrom(t *testing.T) {
	m := newTestModel(t, &fakeSession{})
	long := strings.Repeat("a", 40)
	row := m.renderRow(mail.Summary{From: long, Subject: "s", Seen: true}, false, 80)
	if !strings.Contains(row, "\u2026") {
		t.Errorf("long From should be truncated with ellipsis: %q", row)
	}
}

func TestLoginCommandLoadsInbox(t *testing.T) {
	canned := []mail.Summary{{UID: 7, From: "x@x.com", Subject: "hello", Date: time.Now()}}
	m := newTestModel(t, &fakeSession{summaries: canned})
	m.username = "me@x.com"
	m.password = "pw"
	m.state = loginView

	cmd := m.login()
	msg := cmd()
	ls, ok := msg.(loginSuccess)
	if !ok {
		t.Fatalf("login() returned %T, want loginSuccess", msg)
	}
	if len(ls.emails) != 1 || ls.emails[0].UID != 7 {
		t.Fatalf("loginSuccess emails = %+v", ls.emails)
	}

	mm, _ := m.Update(ls)
	m2 := mm.(*model)
	if m2.state != inboxView {
		t.Errorf("after login state = %v, want inboxView", m2.state)
	}
	if len(m2.emails) != 1 {
		t.Errorf("inbox not populated: %+v", m2.emails)
	}
	if m2.loggedInUser != "me@x.com" {
		t.Errorf("loggedInUser = %q", m2.loggedInUser)
	}
}

func TestFetchMessageMarksSeen(t *testing.T) {
	m := newTestModel(t, &fakeSession{full: &mail.Full{Subject: "hi", Body: "b"}})
	m.emails = []mail.Summary{{UID: 5, Seen: false}}
	m.state = messageView

	cmd := m.fetchMessage(5)
	msg := cmd()
	ml, ok := msg.(messageLoaded)
	if !ok {
		t.Fatalf("fetchMessage() returned %T, want messageLoaded", msg)
	}
	if ml.uid != 5 || ml.email == nil {
		t.Fatalf("messageLoaded = %+v", ml)
	}

	m.Update(ml)
	if m.viewing == nil {
		t.Fatal("viewing not set")
	}
	if !m.emails[0].Seen {
		t.Error("fetched message should be marked seen in the inbox list")
	}
}

func TestSendMailInvokesSession(t *testing.T) {
	fake := &fakeSession{}
	m := newTestModel(t, fake)
	m.username = "me@x.com"
	m.composeTo.SetValue("you@x.com")
	m.composeCc.SetValue("cc@x.com")
	m.composeBcc.SetValue("bcc@x.com")
	m.composeSubject.SetValue("hey")
	m.composeBody = "body text"

	cmd := m.sendMail()
	msg := cmd()
	if _, ok := msg.(sentMsg); !ok {
		t.Fatalf("sendMail() returned %T, want sentMsg", msg)
	}
	if fake.sent == nil {
		t.Fatal("Send was not called on the session")
	}
	if fake.sent.To != "you@x.com" || fake.sent.Body != "body text" {
		t.Errorf("sent = %+v", fake.sent)
	}
	if fake.sent.Cc != "cc@x.com" {
		t.Errorf("Cc = %q, want cc@x.com", fake.sent.Cc)
	}
	if fake.sent.Bcc != "bcc@x.com" {
		t.Errorf("Bcc = %q, want bcc@x.com", fake.sent.Bcc)
	}
}

// TestComposeFieldCycle verifies tab navigates through all five fields
// (To → Cc → Bcc → Subject → Body) and wraps back to To.
func TestComposeFieldCycle(t *testing.T) {
	m := newTestModel(t, &fakeSession{})
	m.state = composeView
	m.composeCursor = 0
	m.composeTo.Focus()

	// To → Cc
	m.advanceComposeField()
	if m.composeCursor != 1 {
		t.Fatalf("after To tab: cursor = %d, want 1", m.composeCursor)
	}
	// Cc → Bcc
	m.advanceComposeField()
	if m.composeCursor != 2 {
		t.Fatalf("after Cc tab: cursor = %d, want 2", m.composeCursor)
	}
	// Bcc → Subject
	m.advanceComposeField()
	if m.composeCursor != 3 {
		t.Fatalf("after Bcc tab: cursor = %d, want 3", m.composeCursor)
	}
	// Subject → Body
	m.advanceComposeField()
	if m.composeCursor != 4 {
		t.Fatalf("after Subject tab: cursor = %d, want 4", m.composeCursor)
	}
	// Body → To (wrap)
	m.advanceComposeField()
	if m.composeCursor != 0 {
		t.Fatalf("after Body tab: cursor = %d, want 0 (wrap)", m.composeCursor)
	}
}

// TestComposeRetreatCycle verifies shift+tab goes backwards through fields.
func TestComposeRetreatCycle(t *testing.T) {
	m := newTestModel(t, &fakeSession{})
	m.state = composeView
	m.composeCursor = 0

	// To → Body (wrap backwards)
	m.retreatComposeField()
	if m.composeCursor != 4 {
		t.Fatalf("retreat from To: cursor = %d, want 4 (wrap)", m.composeCursor)
	}
	// Body → Subject
	m.retreatComposeField()
	if m.composeCursor != 3 {
		t.Fatalf("retreat from Body: cursor = %d, want 3", m.composeCursor)
	}
}

// TestDraftPreservesCcBcc verifies that saving a draft with Cc/Bcc and
// resuming it restores all fields.
func TestDraftPreservesCcBcc(t *testing.T) {
	m := newTestModel(t, &fakeSession{})
	m.state = draftConfirmView
	m.composeTo.SetValue("to@x.com")
	m.composeCc.SetValue("cc@x.com")
	m.composeBcc.SetValue("bcc@x.com")
	m.composeSubject.SetValue("subj")
	m.composeBody = "body"

	// Save draft
	mm, _ := m.Update(runeKey("s"))
	m = mm.(*model)
	if len(m.drafts) != 1 {
		t.Fatalf("expected 1 draft, got %d", len(m.drafts))
	}
	d := m.drafts[0]
	if d.Cc != "cc@x.com" || d.Bcc != "bcc@x.com" {
		t.Errorf("draft Cc=%q Bcc=%q", d.Cc, d.Bcc)
	}

	// Resume the draft
	m.state = draftsView
	m.draftsCursor = 0
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = mm.(*model)
	if m.state != composeView {
		t.Fatalf("expected composeView, got %v", m.state)
	}
	if m.composeCc.Value() != "cc@x.com" {
		t.Errorf("restored Cc = %q", m.composeCc.Value())
	}
	if m.composeBcc.Value() != "bcc@x.com" {
		t.Errorf("restored Bcc = %q", m.composeBcc.Value())
	}
}

// TestNewComposeClearsCcBcc verifies that pressing "c" for a new compose
// starts with empty Cc/Bcc fields.
func TestNewComposeClearsCcBcc(t *testing.T) {
	m := newTestModel(t, &fakeSession{})
	m.state = inboxView
	m.loggedInUser = "me@x.com"
	// Simulate leftover values from a previous compose
	m.composeCc.SetValue("old@x.com")
	m.composeBcc.SetValue("old-bcc@x.com")

	mm, _ := m.Update(runeKey("c"))
	m = mm.(*model)
	if m.state != composeView {
		t.Fatalf("expected composeView, got %v", m.state)
	}
	if m.composeCc.Value() != "" {
		t.Errorf("Cc not cleared: %q", m.composeCc.Value())
	}
	if m.composeBcc.Value() != "" {
		t.Errorf("Bcc not cleared: %q", m.composeBcc.Value())
	}
}
