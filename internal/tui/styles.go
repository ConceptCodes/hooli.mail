package tui

import (
	"github.com/charmbracelet/lipgloss"
	"hooli.mail/server/internal/config"
)

type Styles struct {
	Primary   lipgloss.Style
	Secondary lipgloss.Style
	Muted     lipgloss.Style
	Seal      lipgloss.Style
	Error     lipgloss.Style

	StatusBold   lipgloss.Style
	StatusAccent lipgloss.Style
	Footer       lipgloss.Style
	Cursor       lipgloss.Style
	Group        lipgloss.Style
	Subject      lipgloss.Style
	MetaLabel    lipgloss.Style
	ComposeLabel lipgloss.Style
}

func NewStyles(cfg config.Config) Styles {
	ink := lipgloss.AdaptiveColor{Light: cfg.Theme.Light.Ink, Dark: cfg.Theme.Dark.Ink}
	faint := lipgloss.AdaptiveColor{Light: cfg.Theme.Light.Faint, Dark: cfg.Theme.Dark.Faint}
	seal := lipgloss.AdaptiveColor{Light: cfg.Theme.Light.Seal, Dark: cfg.Theme.Dark.Seal}
	errClr := lipgloss.AdaptiveColor{Light: cfg.Theme.Light.Error, Dark: cfg.Theme.Dark.Error}

	return Styles{
		Primary:   lipgloss.NewStyle().Foreground(ink),
		Secondary: lipgloss.NewStyle().Foreground(faint),
		Muted:     lipgloss.NewStyle().Foreground(faint),
		Seal:      lipgloss.NewStyle().Foreground(seal),
		Error:     lipgloss.NewStyle().Foreground(errClr),

		StatusBold:   lipgloss.NewStyle().Foreground(ink).Bold(true),
		StatusAccent: lipgloss.NewStyle().Foreground(seal).Bold(true),
		Footer:       lipgloss.NewStyle().Foreground(faint),
		Cursor:       lipgloss.NewStyle().Reverse(true),
		Group:        lipgloss.NewStyle().Foreground(faint),
		Subject:      lipgloss.NewStyle().Foreground(ink).Bold(true),

		MetaLabel:    lipgloss.NewStyle().Foreground(faint).Width(6).Align(lipgloss.Right),
		ComposeLabel: lipgloss.NewStyle().Foreground(faint).Width(6).Align(lipgloss.Right),
	}
}
