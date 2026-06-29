package mailbox

import (
	"sort"
	"testing"
	"time"

	"hooli.mail/server/internal/models"
)

// flagsEqual sorts then compares two flag slices so tests are order-
// insensitive — ApplyFlags returns from a map iteration.
func flagsEqual(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	for i := range g {
		if g[i] != w[i] {
			return false
		}
	}
	return true
}

// --- ApplyFlags ---

func TestApplyFlagsSetAll(t *testing.T) {
	t.Parallel()
	current := []string{models.FlagSeen, models.FlagRecent}
	got := ApplyFlags(current, FlagSet, []string{models.FlagSeen, models.FlagFlagged})
	if !flagsEqual(got, models.FlagSeen, models.FlagFlagged) {
		t.Errorf("FlagSet = %v, want [\\Seen \\Flagged] (current must be replaced, not collapsed)", got)
	}
}

func TestApplyFlagsAddPreservesExisting(t *testing.T) {
	t.Parallel()
	current := []string{models.FlagSeen}
	got := ApplyFlags(current, FlagAdd, []string{models.FlagFlagged, models.FlagAnswered})
	if !flagsEqual(got, models.FlagSeen, models.FlagFlagged, models.FlagAnswered) {
		t.Errorf("FlagAdd = %v, want [\\Seen \\Flagged \\Answered]", got)
	}
}

func TestApplyFlagsRemove(t *testing.T) {
	t.Parallel()
	current := []string{models.FlagSeen, models.FlagFlagged, models.FlagRecent}
	got := ApplyFlags(current, FlagRemove, []string{models.FlagSeen, models.FlagRecent})
	if !flagsEqual(got, models.FlagFlagged) {
		t.Errorf("FlagRemove = %v, want [\\Flagged]", got)
	}
}

func TestApplyFlagsSetEmptyClearsAll(t *testing.T) {
	t.Parallel()
	current := []string{models.FlagSeen, models.FlagFlagged}
	got := ApplyFlags(current, FlagSet, nil)
	if len(got) != 0 {
		t.Errorf("FlagSet empty = %v, want []", got)
	}
}

// --- MatchFlags ---

func TestMatchFlags(t *testing.T) {
	t.Parallel()
	flags := []string{models.FlagSeen, models.FlagFlagged}
	if !MatchFlags(flags, []string{models.FlagSeen}, nil) {
		t.Error("expected match when WithFlags=[Seen] and Seen is present")
	}
	if MatchFlags(flags, []string{models.FlagSeen, models.FlagDeleted}, nil) {
		t.Error("expected no match when WithFlags contains absent Deleted")
	}
	if MatchFlags(flags, nil, []string{models.FlagSeen}) {
		t.Error("expected no match when WithoutFlags=[Seen] and Seen is present")
	}
	if !MatchFlags(flags, nil, []string{models.FlagDeleted}) {
		t.Error("expected match when WithoutFlags=[Deleted] and Deleted is absent")
	}
}

func TestMatchFlagsCaseInsensitive(t *testing.T) {
	t.Parallel()
	flags := []string{"\\Seen"}
	if !MatchFlags(flags, []string{"\\SEEN"}, nil) {
		t.Error("flag matching must be case-insensitive")
	}
}

// --- MatchDateRange ---

func TestMatchDateRange(t *testing.T) {
	t.Parallel()
	mid := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	since := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC)
	if !MatchDateRange(mid, since, time.Time{}) {
		t.Error("expected match: mid is after Since")
	}
	if MatchDateRange(since.Add(-24*time.Hour), since, time.Time{}) {
		t.Error("expected no match: date is before Since")
	}
	if !MatchDateRange(mid, time.Time{}, before) {
		t.Error("expected match: mid is before Before")
	}
	if MatchDateRange(before, time.Time{}, before) {
		t.Error("expected no match: date equals Before (Before is exclusive)")
	}
}

// --- MatchSize ---

func TestMatchSize(t *testing.T) {
	t.Parallel()
	if MatchSize(100, 100, 0) {
		t.Error("Larger is exclusive: 100 not larger than 100")
	}
	if !MatchSize(101, 100, 0) {
		t.Error("expected match: 101 > 100")
	}
	if MatchSize(100, 0, 100) {
		t.Error("Smaller is exclusive: 100 not smaller than 100")
	}
	if !MatchSize(99, 0, 100) {
		t.Error("expected match: 99 < 100")
	}
}

// --- Match (full criteria) ---

func TestMatchByText(t *testing.T) {
	t.Parallel()
	email := models.Email{
		From: "alice-mentor@external.com", Subject: "Quarterly review",
		Body: "schedule for Q3", To: []string{"alice@hooli.test"},
	}
	if !Match(email, SearchCriteria{TextContains: []string{"quarterly"}}) {
		t.Error("expected match on 'quarterly' in subject")
	}
	if Match(email, SearchCriteria{TextContains: []string{"nope"}}) {
		t.Error("expected no match on 'nope'")
	}
}

func TestMatchByFlags(t *testing.T) {
	t.Parallel()
	email := models.Email{Flags: []string{models.FlagSeen}}
	if !Match(email, SearchCriteria{WithFlags: []string{models.FlagSeen}}) {
		t.Error("expected match: WithFlags Seen and Seen is present")
	}
	if Match(email, SearchCriteria{WithoutFlags: []string{models.FlagSeen}}) {
		t.Error("expected no match: WithoutFlags Seen but Seen is present")
	}
}

func TestMatchEmptyCriteriaMatchesAll(t *testing.T) {
	t.Parallel()
	email := models.Email{From: "a@b.com", Subject: "anything", Body: "hello"}
	if !Match(email, SearchCriteria{}) {
		t.Error("empty criteria should match every message")
	}
}
