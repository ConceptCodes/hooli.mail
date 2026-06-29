package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	Theme       ThemeConfig `json:"theme"`
	Server      string      `json:"server,omitempty"`
	Insecure    bool        `json:"insecure,omitempty"`
	DateFormat  string      `json:"date_format,omitempty"`
	Signature   string      `json:"signature,omitempty"`
	PollSeconds int         `json:"poll_seconds,omitempty"`
	PageSize    int         `json:"page_size,omitempty"`
}

type ThemeConfig struct {
	Dark  PaletteConfig `json:"dark"`
	Light PaletteConfig `json:"light"`
}

type PaletteConfig struct {
	Ink   string `json:"ink"`
	Dim   string `json:"dim"`   // secondary text — between ink and faint
	Faint string `json:"faint"` // muted text — hints, labels, separators
	Seal  string `json:"seal"`
	Error string `json:"error"`
}

func Default() Config {
	return Config{
		Theme: ThemeConfig{
			Dark: PaletteConfig{
				Ink:   "#ebebeb",
				Dim:   "#999999",
				Faint: "#555555",
				Seal:  "#e58e3c",
				Error: "#e04f5f",
			},
			Light: PaletteConfig{
				Ink:   "#1a1a1a",
				Dim:   "#555555",
				Faint: "#999999",
				Seal:  "#c05a10",
				Error: "#b91c1c",
			},
		},
		DateFormat:  "absolute",
		PollSeconds: 0,
		PageSize:    50,
	}
}

func Path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home dir: %w", err)
	}
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}
	return filepath.Join(xdg, "hoolimail", "config.json"), nil
}

func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Default(), err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Default(), nil
		}
		return Default(), err
	}

	return mergeDefaults(data)
}

func Ensure() (Config, error) {
	path, err := Path()
	if err != nil {
		return Default(), err
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		cfg := Default()
		if err := writeConfig(path, cfg); err != nil {
			return cfg, err
		}
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Default(), err
	}

	cfg, err := mergeDefaults(data)
	if err != nil {
		return cfg, err
	}
	return cfg, nil
}

func writeConfig(path string, cfg Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("cannot create config dir: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("cannot write config: %w", err)
	}

	return nil
}

func mergeDefaults(data []byte) (Config, error) {
	cfg := Default()

	var partial Config
	if err := json.Unmarshal(data, &partial); err != nil {
		return cfg, fmt.Errorf("invalid config file: %w", err)
	}

	if partial.Theme.Dark.Ink != "" {
		cfg.Theme.Dark.Ink = partial.Theme.Dark.Ink
	}
	if partial.Theme.Dark.Dim != "" {
		cfg.Theme.Dark.Dim = partial.Theme.Dark.Dim
	}
	if partial.Theme.Dark.Faint != "" {
		cfg.Theme.Dark.Faint = partial.Theme.Dark.Faint
	}
	if partial.Theme.Dark.Seal != "" {
		cfg.Theme.Dark.Seal = partial.Theme.Dark.Seal
	}
	if partial.Theme.Dark.Error != "" {
		cfg.Theme.Dark.Error = partial.Theme.Dark.Error
	}
	if partial.Theme.Light.Ink != "" {
		cfg.Theme.Light.Ink = partial.Theme.Light.Ink
	}
	if partial.Theme.Light.Dim != "" {
		cfg.Theme.Light.Dim = partial.Theme.Light.Dim
	}
	if partial.Theme.Light.Faint != "" {
		cfg.Theme.Light.Faint = partial.Theme.Light.Faint
	}
	if partial.Theme.Light.Seal != "" {
		cfg.Theme.Light.Seal = partial.Theme.Light.Seal
	}
	if partial.Theme.Light.Error != "" {
		cfg.Theme.Light.Error = partial.Theme.Light.Error
	}
	if partial.Server != "" {
		cfg.Server = partial.Server
	}
	if partial.Insecure {
		cfg.Insecure = true
	}
	if partial.DateFormat != "" {
		cfg.DateFormat = partial.DateFormat
	}
	if partial.Signature != "" {
		cfg.Signature = partial.Signature
	}
	if partial.PollSeconds > 0 {
		cfg.PollSeconds = partial.PollSeconds
	}
	if partial.PageSize > 0 {
		cfg.PageSize = partial.PageSize
	}

	return cfg, nil
}
