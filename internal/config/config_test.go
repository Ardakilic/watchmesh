package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeFile creates parent dirs and writes content to path.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// validJSON is a minimal config exercising interval, env expansion, and syncs.
const validJSON = `{
  "interval": "15m",
  "database_url": "env:TEST_WM_DB",
  "connections": {
    "trakt_main": {"type": "trakt", "client_id": "env:TEST_WM_ID", "client_secret": "x"},
    "trakt_alt": {"type": "trakt", "client_id": "y"},
    "simkl_main": {"type": "simkl", "client_id": "z"}
  },
  "syncs": [{"name": "main", "source": "trakt_main", "targets": ["simkl_main"]}]
}`

// TestLoad verifies file parsing, defaults, and env: expansion.
func TestLoad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	writeFile(t, p, validJSON)
	t.Setenv("TEST_WM_DB", "postgres://db")
	t.Setenv("TEST_WM_ID", "expanded")
	unsetEnv(t, "DATABASE_URL")
	unsetEnv(t, "WATCHMESH_INTERVAL")

	cfg, err := Load(p, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Interval != 15*time.Minute {
		t.Fatalf("interval = %v", cfg.Interval)
	}
	if cfg.DatabaseURL != "postgres://db" {
		t.Fatalf("database_url = %q", cfg.DatabaseURL)
	}
	if cfg.Connections["trakt_main"].ClientID != "expanded" {
		t.Fatalf("env: not expanded: %q", cfg.Connections["trakt_main"].ClientID)
	}
}

// TestLoadPrecedence verifies flag > env > file precedence.
func TestLoadPrecedence(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	writeFile(t, p, `{"interval":"15m","database_url":"file-db"}`)
	t.Setenv("WATCHMESH_INTERVAL", "30m")
	t.Setenv("DATABASE_URL", "env-db")

	cfg, err := Load(p, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Interval != 30*time.Minute || cfg.DatabaseURL != "env-db" {
		t.Fatalf("env should beat file: %v %q", cfg.Interval, cfg.DatabaseURL)
	}

	cfg, err = Load(p, "1h", "flag-db")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Interval != time.Hour || cfg.DatabaseURL != "flag-db" {
		t.Fatalf("flag should beat env: %v %q", cfg.Interval, cfg.DatabaseURL)
	}
}

// TestValidate verifies duplicate types pass while unknown refs fail.
func TestValidate(t *testing.T) {
	ok := &Config{
		Connections: map[string]Connection{
			"trakt_main": {Type: "trakt"},
			"trakt_alt":  {Type: "trakt"},
			"simkl_main": {Type: "simkl"},
		},
		Syncs: []Sync{{Name: "m", Source: "trakt_main", Targets: []string{"trakt_alt", "simkl_main"}}},
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("duplicate types must be allowed: %v", err)
	}

	badSource := &Config{
		Connections: map[string]Connection{"a": {}},
		Syncs:       []Sync{{Name: "m", Source: "ghost", Targets: []string{"a"}}},
	}
	if err := badSource.Validate(); err == nil {
		t.Fatal("unknown source should fail")
	}

	badTarget := &Config{
		Connections: map[string]Connection{"a": {}},
		Syncs:       []Sync{{Name: "m", Source: "a", Targets: []string{"ghost"}}},
	}
	if err := badTarget.Validate(); err == nil {
		t.Fatal("unknown target should fail")
	}
}

// TestHome verifies config path resolution order: flag > env > XDG > std > legacy.
func TestHome(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("WATCHMESH_CONFIG", "")

	xdgPath := filepath.Join(xdg, "watchmesh", "config.json")
	stdPath := filepath.Join(home, ".config", "watchmesh", "config.json")
	legacyPath := filepath.Join(home, ".watchmesh", "config.json")

	// Nothing exists: writes go XDG.
	if got := ResolveConfigPath(""); got != xdgPath {
		t.Fatalf("empty search should return XDG write path, got %q", got)
	}

	// Legacy fallback picked when alone.
	writeFile(t, legacyPath, `{}`)
	if got := ResolveConfigPath(""); got != legacyPath {
		t.Fatalf("legacy fallback, got %q", got)
	}

	// ~/.config beats legacy.
	writeFile(t, stdPath, `{}`)
	if got := ResolveConfigPath(""); got != stdPath {
		t.Fatalf("std should beat legacy, got %q", got)
	}

	// XDG beats ~/.config.
	writeFile(t, xdgPath, `{}`)
	if got := ResolveConfigPath(""); got != xdgPath {
		t.Fatalf("xdg should win, got %q", got)
	}

	// Env beats files.
	envPath := filepath.Join(home, "custom.json")
	writeFile(t, envPath, `{}`)
	t.Setenv("WATCHMESH_CONFIG", envPath)
	if got := ResolveConfigPath(""); got != envPath {
		t.Fatalf("env should win, got %q", got)
	}

	// Flag beats env.
	if got := ResolveConfigPath("/flag.json"); got != "/flag.json" {
		t.Fatalf("flag should win, got %q", got)
	}
}
