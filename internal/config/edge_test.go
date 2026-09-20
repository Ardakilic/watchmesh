package config

import (
	"os"
	"path/filepath"
	"testing"
)

// unsetEnv clears key for the test, restoring it on cleanup.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	if v, ok := os.LookupEnv(key); ok {
		t.Cleanup(func() { _ = os.Setenv(key, v) })
	} else {
		t.Cleanup(func() { _ = os.Unsetenv(key) })
	}
	_ = os.Unsetenv(key)
}

// TestDefaultWritePathBranches verifies XDG vs home-dir write paths.
func TestDefaultWritePathBranches(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	if got := DefaultWritePath(); got != filepath.Join("/tmp/xdg", "watchmesh", "config.json") {
		t.Fatalf("xdg: got %q", got)
	}
	unsetEnv(t, "XDG_CONFIG_HOME")
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	if got := DefaultWritePath(); got != filepath.Join(home, ".config", "watchmesh", "config.json") {
		t.Fatalf("home: got %q", got)
	}
}

// TestLoadMissingExplicit verifies an explicit missing path fails.
func TestLoadMissingExplicit(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.json"), "", ""); err == nil {
		t.Fatal("missing explicit path must fail")
	}
}

// TestLoadMalformed verifies invalid JSON fails.
func TestLoadMalformed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, p, `not json`)
	if _, err := Load(p, "", ""); err == nil {
		t.Fatal("malformed must fail")
	}
}

// TestLoadNoFileDefaults verifies defaults apply with no config file.
func TestLoadNoFileDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	unsetEnv(t, "WATCHMESH_CONFIG")
	unsetEnv(t, "WATCHMESH_INTERVAL")
	unsetEnv(t, "DATABASE_URL")
	cfg, err := Load("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Interval != DefaultInterval {
		t.Fatalf("interval = %v want %v", cfg.Interval, DefaultInterval)
	}
}

// TestLoadBadInterval verifies an unparseable interval fails.
func TestLoadBadInterval(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, p, `{"interval":"bogus"}`)
	unsetEnv(t, "WATCHMESH_INTERVAL")
	if _, err := Load(p, "", ""); err == nil {
		t.Fatal("bad interval must fail")
	}
}

// TestLoadValidateError verifies Load surfaces Validate failures.
func TestLoadValidateError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, p, `{"connections":{"a":{"type":"trakt"}},"syncs":[{"name":"m","source":"ghost","targets":["a"]}]}`)
	if _, err := Load(p, "", ""); err == nil {
		t.Fatal("unknown source must fail")
	}
}

// TestValidateSyncNames verifies empty/duplicate sync names fail.
func TestValidateSyncNames(t *testing.T) {
	base := &Config{Connections: map[string]Connection{"a": {Type: "trakt"}}}
	if err := (&Config{
		Connections: base.Connections,
		Syncs:       []Sync{{Name: "", Source: "a", Targets: []string{"a"}}},
	}).Validate(); err == nil {
		t.Fatal("empty sync name must fail")
	}
	dup := &Config{
		Connections: base.Connections,
		Syncs: []Sync{
			{Name: "m", Source: "a", Targets: []string{"a"}},
			{Name: "m", Source: "a", Targets: []string{"a"}},
		},
	}
	if err := dup.Validate(); err == nil {
		t.Fatal("duplicate sync name must fail")
	}
	ok := &Config{
		Connections: base.Connections,
		Syncs:       []Sync{{Name: "m", Source: "a", Targets: []string{"a"}}},
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("uniquely named sync must pass: %v", err)
	}
}

// TestDefaultWritePathFallback verifies the relative fallback when XDG and HOME are empty.
func TestDefaultWritePathFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	got := DefaultWritePath()
	if got != filepath.Join(".config", "watchmesh", "config.json") {
		t.Fatalf("got %q want %q", got, filepath.Join(".config", "watchmesh", "config.json"))
	}
}
