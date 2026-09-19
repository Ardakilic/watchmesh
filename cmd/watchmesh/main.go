// Command watchmesh syncs watch history across services.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/watchmesh/watchmesh/internal/config"
	"github.com/watchmesh/watchmesh/internal/engine"
	"github.com/watchmesh/watchmesh/internal/ryot"
	"github.com/watchmesh/watchmesh/internal/simkl"
	"github.com/watchmesh/watchmesh/internal/store"
	"github.com/watchmesh/watchmesh/internal/trakt"
	"github.com/watchmesh/watchmesh/internal/yamtrack"
)

// main is the watchmesh entrypoint; a run error becomes exit 1.
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "watchmesh:", err)
		os.Exit(1)
	}
}

// usage prints the subcommand summary to stderr.
func usage() {
	fmt.Fprintln(os.Stderr, "Usage: watchmesh <sync|serve|auth> [flags]")
	fmt.Fprintln(os.Stderr, "  sync  --sync <name> (empty=all)")
	fmt.Fprintln(os.Stderr, "  serve (ticker loop)")
	fmt.Fprintln(os.Stderr, "  auth  --connection <name>")
}

// run dispatches sync|serve|auth; args excludes the program name.
func run(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("missing command")
	}
	switch args[0] {
	case "sync":
		return runSync(args[1:])
	case "serve":
		return runServe(args[1:])
	case "auth":
		return runAuth(args[1:])
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// wire maps one connection to its Source+Target (same instance).
func wire(c config.Connection) (engine.Source, engine.Target, error) {
	switch c.Type {
	case "trakt":
		cl := trakt.New("", c.ClientID, c.ClientSecret, c.Token)
		return cl, cl, nil
	case "simkl":
		cl := simkl.New("", c.ClientID, c.Token)
		return cl, cl, nil
	case "ryot":
		cl := ryot.New(c.BaseURL, c.Token)
		return cl, cl, nil
	case "yamtrack":
		cl := yamtrack.New(c.BaseURL, c.Token)
		return cl, cl, nil
	default:
		return nil, nil, fmt.Errorf("unknown connection type %q", c.Type)
	}
}

// boot loads config, applies migrations, and opens the store.
func boot(configPath, migrationsPath string) (*config.Config, *store.Store, error) {
	cfg, err := config.Load(configPath, "", "")
	if err != nil {
		return nil, nil, err
	}
	if cfg.DatabaseURL == "" {
		return nil, nil, fmt.Errorf("missing database_url (set DATABASE_URL or config database_url)")
	}
	if err := store.MigrateUp(cfg.DatabaseURL, migrationsPath); err != nil {
		return nil, nil, err
	}
	st, err := store.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	return cfg, st, nil
}

// runOne wires and syncs one named sync definition end to end.
func runOne(ctx context.Context, cfg *config.Config, st *store.Store, name string) error {
	var def *config.Sync
	for i := range cfg.Syncs {
		if cfg.Syncs[i].Name == name {
			def = &cfg.Syncs[i]
			break
		}
	}
	if def == nil {
		return fmt.Errorf("unknown sync %q", name)
	}
	srcConn, ok := cfg.Connections[def.Source]
	if !ok {
		return fmt.Errorf("sync %q: unknown source %q", def.Name, def.Source)
	}
	src, _, err := wire(srcConn)
	if err != nil {
		return fmt.Errorf("sync %q: %w", def.Name, err)
	}
	targets := make([]engine.Target, 0, len(def.Targets))
	for _, tn := range def.Targets {
		tc, ok := cfg.Connections[tn]
		if !ok {
			return fmt.Errorf("sync %q: unknown target %q", def.Name, tn)
		}
		_, tgt, err := wire(tc)
		if err != nil {
			return fmt.Errorf("sync %q: %w", def.Name, err)
		}
		targets = append(targets, tgt)
	}
	return engine.Sync(ctx, def.Name, src, targets, st)
}

// runSync runs one (--sync) or all sync definitions once.
func runSync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to config file")
	migPath := fs.String("migrations-path", "", "external migrations dir (file:// override for dev)")
	syncName := fs.String("sync", "", "sync name to run (empty=all)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: watchmesh sync [--config path] [--migrations-path dir] [--sync name]\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	ctx := context.Background()
	cfg, st, err := boot(*configPath, *migPath)
	if err != nil {
		return err
	}
	defer st.Close()
	names := make([]string, 0, len(cfg.Syncs))
	for _, s := range cfg.Syncs {
		if *syncName == "" || s.Name == *syncName {
			names = append(names, s.Name)
		}
	}
	if *syncName != "" && len(names) == 0 {
		return fmt.Errorf("unknown sync %q", *syncName)
	}
	failed := false
	for _, n := range names {
		if err := runOne(ctx, cfg, st, n); err != nil {
			slog.Error("sync failed", "sync", n, "err", err)
			failed = true
		} else {
			slog.Info("sync ok", "sync", n)
		}
	}
	if failed {
		return fmt.Errorf("sync failed")
	}
	return nil
}

// runServe ticks runAll on cfg.Interval until SIGINT/SIGTERM.
func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to config file")
	migPath := fs.String("migrations-path", "", "external migrations dir (file:// override for dev)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: watchmesh serve [--config path] [--migrations-path dir]\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	cfg, st, err := boot(*configPath, *migPath)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	interval := cfg.Interval
	if interval <= 0 {
		interval = config.DefaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	slog.Info("serving", "interval", interval)
	runAll(ctx, cfg, st)
	for {
		select {
		case <-ctx.Done():
			slog.Info("shutting down")
			return nil
		case <-ticker.C:
			runAll(ctx, cfg, st)
		}
	}
}

// runAll runs every configured sync, logging per-sync failures.
func runAll(ctx context.Context, cfg *config.Config, st *store.Store) {
	for _, s := range cfg.Syncs {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := runOne(ctx, cfg, st, s.Name); err != nil {
			slog.Error("sync failed", "sync", s.Name, "err", err)
		} else {
			slog.Info("sync ok", "sync", s.Name)
		}
	}
}

// runAuth authenticates one --connection: device/PIN flow or token check.
func runAuth(args []string) error {
	fs := flag.NewFlagSet("auth", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to config file")
	_ = fs.String("migrations-path", "", "external migrations dir (unused for auth)")
	connName := fs.String("connection", "", "connection name to authenticate")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: watchmesh auth [--config path] --connection <name>\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if *connName == "" {
		return fmt.Errorf("missing --connection")
	}
	cfg, err := config.Load(*configPath, "", "")
	if err != nil {
		return err
	}
	conn, ok := cfg.Connections[*connName]
	if !ok {
		return fmt.Errorf("unknown connection %q", *connName)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	switch conn.Type {
	case "trakt":
		cl := trakt.New("", conn.ClientID, conn.ClientSecret, conn.Token)
		tok, err := cl.DeviceAuth(ctx, os.Stdout)
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "OK token:", tok)
		return nil
	case "simkl":
		cl := simkl.New("", conn.ClientID, conn.Token)
		tok, err := cl.PINAuth(ctx, os.Stdout)
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "OK token:", tok)
		return nil
	case "ryot":
		cl := ryot.New(conn.BaseURL, conn.Token)
		if err := cl.ValidateToken(ctx); err != nil {
			fmt.Fprintln(os.Stdout, "FAIL:", err)
			return err
		}
		fmt.Fprintln(os.Stdout, "OK")
		return nil
	case "yamtrack":
		cl := yamtrack.New(conn.BaseURL, conn.Token)
		if err := cl.ValidateToken(ctx); err != nil {
			fmt.Fprintln(os.Stdout, "FAIL:", err)
			return err
		}
		fmt.Fprintln(os.Stdout, "OK")
		return nil
	default:
		return fmt.Errorf("unknown connection type %q", conn.Type)
	}
}
