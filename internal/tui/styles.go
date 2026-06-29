package tui

import (
	"github.com/charmbracelet/lipgloss"
	"hooli.mail/server/internal/config"
)

// Styles holds every named lipgloss style used across the TUI. Three text
// tiers give the hierarchy room to breathe: Primary (ink) reads first,
// Secondary (dim) recedes slightly, Muted (faint) disappears into structure.
//
// The seal color is used in exactly two places: the wax-seal unread indicator
// (██) and the cursor gutter (▌). Brass means "attention here" — nothing else.
type Styles struct {
	// Text hierarchy — three tiers
	Primary   lipgloss.Style // ink: sender names, subjects when unread, body text
	Secondary lipgloss.Style // dim: subjects when read, meta values, back links
	Muted     lipgloss.Style // faint: dates, hints, separators, group rules

	// Accent
	Seal      lipgloss.Style // brass: wax-seal on unread rows
	CursorBar lipgloss.Style // brass: ▌ gutter on the selected row
	Error     lipgloss.Style // red: inline error messages

	// Status bar
	StatusBold   lipgloss.Style // ink + bold: section label ("INBOX")
	StatusAccent lipgloss.Style // brass + bold: message count

	// List chrome
	Footer lipgloss.Style // dim: key-hint bar at the bottom
	Cursor lipgloss.Style // reverse: full-row selection highlight
	Group  lipgloss.Style // dim: date-group header labels

	// Message chrome
	Subject lipgloss.Style // ink + bold: open-message subject line
	MetaKey lipgloss.Style // faint, right-aligned, fixed-width: "From:" labels
	MetaVal lipgloss.Style // dim: meta field values

	// Compose chrome
	ComposeLabel lipgloss.Style // faint, right-aligned, fixed-width: field labels

	// Modal
	ModalBox lipgloss.Style // rounded border box for overlay dialogs
}

func NewStyles(cfg config.Config) Styles {
	ink    := lipgloss.AdaptiveColor{Light: cfg.Theme.Light.Ink,   Dark: cfg.Theme.Dark.Ink}
	dim    := lipgloss.AdaptiveColor{Light: cfg.Theme.Light.Dim,   Dark: cfg.Theme.Dark.Dim}
	faint  := lipgloss.AdaptiveColor{Light: cfg.Theme.Light.Faint, Dark: cfg.Theme.Dark.Faint}
	seal   := lipgloss.AdaptiveColor{Light: cfg.Theme.Light.Seal,  Dark: cfg.Theme.Dark.Seal}
	errClr := lipgloss.AdaptiveColor{Light: cfg.Theme.Light.Error, Dark: cfg.Theme.Dark.Error}

	return Styles{
		Primary:   lipgloss.NewStyle().Foreground(ink),
		Secondary: lipgloss.NewStyle().Foreground(dim),
		Muted:     lipgloss.NewStyle().Foreground(faint),

		Seal:      lipgloss.NewStyle().Foreground(seal),
		CursorBar: lipgloss.NewStyle().Foreground(seal),
		Error:     lipgloss.NewStyle().Foreground(errClr),

		StatusBold:   lipgloss.NewStyle().Foreground(ink).Bold(true),
		StatusAccent: lipgloss.NewStyle().Foreground(seal).Bold(true),

		Footer: lipgloss.NewStyle().Foreground(faint),
		Cursor: lipgloss.NewStyle().Reverse(true),
		Group:  lipgloss.NewStyle().Foreground(dim),

		Subject: lipgloss.NewStyle().Foreground(ink).Bold(true),
		MetaKey: lipgloss.NewStyle().Foreground(faint).Width(6).Align(lipgloss.Right),
		MetaVal: lipgloss.NewStyle().Foreground(dim),

		ComposeLabel: lipgloss.NewStyle().Foreground(faint).Width(6).Align(lipgloss.Right),

		ModalBox: lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(faint).
			Padding(1, 4),
	}
}
