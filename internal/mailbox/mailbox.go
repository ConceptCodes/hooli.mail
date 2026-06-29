// Package mailbox owns the semantic rules of a mail mailbox: search criteria
// evaluation, flag mutation, and date/size matching. These rules are
// protocol-agnostic — they describe what "this message matches this search"
// or "apply this flag operation" means, independent of IMAP or SMTP wire
// formats. The protocol adapters translate their wire-level types into these
// domain types; the storage adapters use these helpers to implement bulk
// operations. This gives UID, sequence-number, ordering, and atomicity rules
// a single home with one semantic test suite.
package mailbox

import (
	"strings"
	"time"

	"hooli.mail/server/internal/models"
)

// SearchCriteria is a protocol-agnostic description of which messages match.
// Every field is optional (zero value = no constraint). A message matches
// only if it satisfies every specified field.
type SearchCriteria struct {
	WithFlags    []string
	WithoutFlags []string
	Since        time.Time // internal date >= Since
	Before       time.Time // internal date < Before (exclusive)
	Larger       uint32    // size > Larger (exclusive)
	Smaller      uint32    // size < Smaller (exclusive)
	BodyContains []string  // substring match on body text (case-insensitive)
	TextContains []string  // substring match on From/To/Subject/Body
	HeaderMatch  map[string][]string
}

// Match evaluates whether an email satisfies every constraint in the
// criteria. Unsupported header names are treated as match (fail open) so
// that unknown criteria do not silently hide messages.
func Match(email models.Email, c SearchCriteria) bool {
	if !MatchFlags(email.Flags, c.WithFlags, c.WithoutFlags) {
		return false
	}
	if !MatchDateRange(email.Date, c.Since, c.Before) {
		return false
	}
	if !MatchSize(email.Size, c.Larger, c.Smaller) {
		return false
	}
	for _, needle := range c.BodyContains {
		if !strings.Contains(strings.ToLower(email.Body), strings.ToLower(needle)) {
			return false
		}
	}
	for _, needle := range c.TextContains {
		lower := strings.ToLower(needle)
		if !strings.Contains(strings.ToLower(email.From), lower) &&
			!strings.Contains(strings.ToLower(email.Subject), lower) &&
			!strings.Contains(strings.ToLower(email.Body), lower) &&
			!containsAny(email.To, lower) {
			return false
		}
	}
	for header, values := range c.HeaderMatch {
		switch strings.ToLower(header) {
		case "from":
			for _, v := range values {
				if !strings.Contains(strings.ToLower(email.From), strings.ToLower(v)) {
					return false
				}
			}
		case "to":
			for _, v := range values {
				if !containsAny(email.To, strings.ToLower(v)) {
					return false
				}
			}
		case "subject":
			for _, v := range values {
				if !strings.Contains(strings.ToLower(email.Subject), strings.ToLower(v)) {
					return false
				}
			}
		}
	}
	return true
}

// MatchFlags checks that every flag in `with` is present and every flag in
// `without` is absent. Comparison is case-insensitive.
func MatchFlags(flags []string, with, without []string) bool {
	has := make(map[string]bool, len(flags))
	for _, f := range flags {
		has[strings.ToLower(f)] = true
	}
	for _, f := range with {
		if !has[strings.ToLower(f)] {
			return false
		}
	}
	for _, f := range without {
		if has[strings.ToLower(f)] {
			return false
		}
	}
	return true
}

// MatchDateRange checks that t falls within [since, before). Zero values for
// since or before mean "no lower/upper bound".
func MatchDateRange(t, since, before time.Time) bool {
	if !since.IsZero() && t.Before(since) {
		return false
	}
	if !before.IsZero() && !t.Before(before) {
		return false
	}
	return true
}

// MatchSize checks that size falls within (larger, smaller). Zero values mean
// "no bound". Both bounds are exclusive, matching RFC 3501 LARGER/SMALLER.
func MatchSize(size int, larger, smaller uint32) bool {
	if larger > 0 && uint32(size) <= larger {
		return false
	}
	if smaller > 0 && uint32(size) >= smaller {
		return false
	}
	return true
}

func containsAny(addrs []string, needle string) bool {
	for _, a := range addrs {
		if strings.Contains(strings.ToLower(a), needle) {
			return true
		}
	}
	return false
}

// FlagOperation defines how ApplyFlags mutates a flag set.
type FlagOperation int

const (
	// FlagSet replaces the entire flag set with the given flags.
	FlagSet FlagOperation = iota
	// FlagAdd adds the given flags to the current set.
	FlagAdd
	// FlagRemove removes the given flags from the current set.
	FlagRemove
)

// ApplyFlags computes the result of applying op with flags to the current
// set. The input slice is not modified. The result order is unspecified
// (callers that need determinism should sort).
//
// FlagSet replaces the flag set atomically — it must not be confused with
// per-flag add, which would collapse to only the last flag if done inside
// a loop.
func ApplyFlags(current []string, op FlagOperation, flags []string) []string {
	flagSet := make(map[string]bool)
	for _, f := range current {
		flagSet[f] = true
	}

	switch op {
	case FlagSet:
		flagSet = make(map[string]bool, len(flags))
		for _, f := range flags {
			flagSet[f] = true
		}
	case FlagAdd:
		for _, f := range flags {
			flagSet[f] = true
		}
	case FlagRemove:
		for _, f := range flags {
			delete(flagSet, f)
		}
	}

	result := make([]string, 0, len(flagSet))
	for f := range flagSet {
		result = append(result, f)
	}
	return result
}
