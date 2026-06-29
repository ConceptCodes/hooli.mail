package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeConfigAt writes the given JSON content to path, creating directories.
func writeConfigAt(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestMergeDefaultsAppliesPartial confirms that the verbose per-field merge
// is not dropped as part of any future refactor toward json.Unmarshal-over-
// defaults.
func TestMergeDefaultsAppliesPartial(t *testing.T) {
	raw := []byte(`{
		"theme": {"dark": {"ink": "#ffffff"}},
		"server": "mail.example.com",
		"page_size": 100
	}`)
	cfg, err := mergeDefaults(raw)
	if err != nil {
		t.Fatalf("mergeDefaults: %v", err)
	}
	if cfg.Theme.Dark.Ink != "#ffffff" {
		t.Errorf("Dark.Ink = %q, want #ffffff", cfg.Theme.Dark.Ink)
	}
	// Untouched palette fields must fall through from Default().
	if cfg.Theme.Dark.Dim == "" {
		t.Error("Dark.Dim is empty; want default to fall through")
	}
	if cfg.Server != "mail.example.com" {
		t.Errorf("Server = %q", cfg.Server)
	}
	if cfg.PageSize != 100 {
		t.Errorf("PageSize = %d, want 100", cfg.PageSize)
	}
}

// TestMergeDefaultsRejectsInvalidJSON ensures the error path returns an
// error. The previous Ensure() implementation swallowed this error and
// returned a partially-zero Config with err=nil — meaning a broken config
// file silently downgraded the user to defaults.
func TestMergeDefaultsRejectsInvalidJSON(t *testing.T) {
	_, err := mergeDefaults([]byte("not valid json {"))
	if err == nil {
		t.Fatal("mergeDefaults returned nil error on invalid JSON")
	}
}

// TestEnsureSurfacesMergeError pins the bug fix at config.go:107: previously
// when mergeDefaults returned an error, Ensure discarded it and returned
// nil. Now the caller sees the parse failure and can decide what to do.
func TestEnsureSurfacesMergeError(t *testing.T) {
	dir := t.TempDir()
	xdgHome := filepath.Join(dir, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdgHome)

	path, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	writeConfigAt(t, path, "{ totally broken")

	_, err = Ensure()
	if err == nil {
		t.Fatal("Ensure returned nil for a malformed config file; want parse error")
	}
}

// TestEnsureWritesDefaultWhenAbsent confirms that a fresh install gets a
// usable default config written to disk.
func TestEnsureWritesDefaultWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfg, err := Ensure()
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if cfg.Theme.Dark.Ink == "" {
		t.Error("default Theme.Dark.Ink is empty after Ensure")
	}
	if _, err := os.Stat(filepath.Join(dir, "hoolimail", "config.json")); err != nil {
		t.Errorf("config file not written: %v", err)
	}
}

// TestLoadMissingFileReturnsDefault confirms the no-config-yet path: Load
// must not error when the file simply doesn't exist yet.
func TestLoadMissingFileReturnsDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir()))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if cfg.PageSize != 50 {
		t.Errorf("PageSize = %d, want default 50", cfg.PageSize)
	}
}

// TestRoundTripDefaultJSON confirms that Default() always marshals cleanly,
// guarding against future field additions that don't carry json tags.
func TestRoundTripDefaultJSON(t *testing.T) {
	data, err := json.MarshalIndent(Default(), "", "  ")
	if err != nil {
		t.Fatalf("marshal default: %v", err)
	}
	var round Config
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatalf("unmarshal default: %v", err)
	}
	if round.PageSize != 50 {
		t.Errorf("PageSize round-trip = %d", round.PageSize)
	}
}
