package config

import (
	"os"
	"path/filepath"
	"testing"
)

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	if v, ok := os.LookupEnv(key); ok {
		t.Cleanup(func() { _ = os.Setenv(key, v) })
	} else {
		t.Cleanup(func() { _ = os.Unsetenv(key) })
	}
	_ = os.Unsetenv(key)
}

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

func TestLoadMissingExplicit(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.json"), "", ""); err == nil {
		t.Fatal("missing explicit path must fail")
	}
}

func TestLoadMalformed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, p, `not json`)
	if _, err := Load(p, "", ""); err == nil {
		t.Fatal("malformed must fail")
	}
}

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

func TestLoadBadInterval(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, p, `{"interval":"bogus"}`)
	unsetEnv(t, "WATCHMESH_INTERVAL")
	if _, err := Load(p, "", ""); err == nil {
		t.Fatal("bad interval must fail")
	}
}

func TestLoadValidateError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, p, `{"connections":{"a":{"type":"trakt"}},"syncs":[{"name":"m","source":"ghost","targets":["a"]}]}`)
	if _, err := Load(p, "", ""); err == nil {
		t.Fatal("unknown source must fail")
	}
}
