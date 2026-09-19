// Package config loads watchmesh JSON configuration.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultInterval applies when no flag, env, or file value sets interval.
const DefaultInterval = 15 * time.Minute

// Connection is one named service account; routing is by name.
type Connection struct {
	Type         string `json:"type"`
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
	BaseURL      string `json:"base_url,omitempty"`
	Token        string `json:"token,omitempty"`
}

// Sync wires one source connection to one or more target connections.
type Sync struct {
	Name    string   `json:"name"`
	Source  string   `json:"source"`
	Targets []string `json:"targets"`
}

// Config is the resolved, env-expanded configuration.
type Config struct {
	Interval    time.Duration
	DatabaseURL string
	Connections map[string]Connection
	Syncs       []Sync
}

type rawConfig struct {
	Interval    string                `json:"interval"`
	DatabaseURL string                `json:"database_url"`
	Connections map[string]Connection `json:"connections"`
	Syncs       []Sync                `json:"syncs"`
}

// ResolveConfigPath returns the config file to read:
// flag > WATCHMESH_CONFIG > $XDG_CONFIG_HOME/watchmesh/config.json >
// ~/.config/watchmesh/config.json > legacy ~/.watchmesh/config.json.
// First existing file wins; when none exists the XDG write path is returned.
func ResolveConfigPath(flagPath string) string {
	if flagPath != "" {
		return flagPath
	}
	if v := os.Getenv("WATCHMESH_CONFIG"); v != "" {
		return v
	}
	for _, p := range searchPaths() {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return DefaultWritePath()
}

// DefaultWritePath is where new config/state writes go (XDG path).
func DefaultWritePath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "watchmesh", "config.json")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "watchmesh", "config.json")
	}
	return filepath.Join(".config", "watchmesh", "config.json")
}

func searchPaths() []string {
	var out []string
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		out = append(out, filepath.Join(xdg, "watchmesh", "config.json"))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = append(out,
			filepath.Join(home, ".config", "watchmesh", "config.json"),
			filepath.Join(home, ".watchmesh", "config.json"),
		)
	}
	return out
}

// expand resolves "env:VAR" references, passing other strings through.
func expand(s string) string {
	if v, ok := strings.CutPrefix(s, "env:"); ok {
		return os.Getenv(v)
	}
	return s
}

// Load reads the resolved config file and applies
// flag > env > file > default precedence for interval, database_url,
// and the config path itself (see ResolveConfigPath).
// Env names: WATCHMESH_INTERVAL, DATABASE_URL.
func Load(flagPath, flagInterval, flagDatabaseURL string) (*Config, error) {
	path := ResolveConfigPath(flagPath)
	var raw rawConfig
	data, err := os.ReadFile(path)
	if err != nil {
		if flagPath == "" && os.Getenv("WATCHMESH_CONFIG") == "" && os.IsNotExist(err) {
			// No config file on the search path: fall through to defaults.
		} else {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
	} else if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	intervalStr := raw.Interval
	if intervalStr == "" {
		intervalStr = DefaultInterval.String()
	}
	if v := os.Getenv("WATCHMESH_INTERVAL"); v != "" {
		intervalStr = v
	}
	if flagInterval != "" {
		intervalStr = flagInterval
	}
	interval, err := time.ParseDuration(expand(intervalStr))
	if err != nil {
		return nil, fmt.Errorf("invalid interval %q: %w", intervalStr, err)
	}

	db := expand(raw.DatabaseURL)
	if v := os.Getenv("DATABASE_URL"); v != "" {
		db = v
	}
	if flagDatabaseURL != "" {
		db = flagDatabaseURL
	}

	conns := make(map[string]Connection, len(raw.Connections))
	for name, c := range raw.Connections {
		c.ClientID = expand(c.ClientID)
		c.ClientSecret = expand(c.ClientSecret)
		c.BaseURL = expand(c.BaseURL)
		c.Token = expand(c.Token)
		conns[name] = c
	}

	cfg := &Config{
		Interval:    interval,
		DatabaseURL: db,
		Connections: conns,
		Syncs:       raw.Syncs,
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate fails fast on unknown source/target names and on empty or
// duplicate sync names. Duplicate types under distinct names are allowed.
func (c *Config) Validate() error {
	seen := make(map[string]struct{}, len(c.Syncs))
	for _, s := range c.Syncs {
		if s.Name == "" {
			return fmt.Errorf("sync with empty name")
		}
		if _, dup := seen[s.Name]; dup {
			return fmt.Errorf("duplicate sync %q", s.Name)
		}
		seen[s.Name] = struct{}{}
		if _, ok := c.Connections[s.Source]; !ok {
			return fmt.Errorf("sync %q: unknown source %q", s.Name, s.Source)
		}
		for _, t := range s.Targets {
			if _, ok := c.Connections[t]; !ok {
				return fmt.Errorf("sync %q: unknown target %q", s.Name, t)
			}
		}
	}
	return nil
}
