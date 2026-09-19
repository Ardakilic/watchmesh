// Package store persists sync cursors in Postgres.
package store

import (
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	migrations "github.com/watchmesh/watchmesh/migrations"
)

// MigrateUp applies pending migrations before the store opens.
// migrationsPath empty means embedded iofs; set means file:// override for dev.
// ErrNoChange continues; any other error (incl. dirty) is fatal with the
// version in the message. Never auto-Forces: recover manually with
// `migrate force <last_good>` then restart.
func MigrateUp(databaseURL, migrationsPath string) error {
	var m *migrate.Migrate
	var err error
	if migrationsPath != "" {
		m, err = migrate.New("file://"+migrationsPath, databaseURL)
	} else {
		src, err2 := iofs.New(migrations.FS, ".")
		if err2 != nil {
			return fmt.Errorf("migrate source: %w", err2)
		}
		m, err = migrate.NewWithSourceInstance("iofs", src, databaseURL)
	}
	if err != nil {
		return fmt.Errorf("migrate open: %w", err)
	}
	defer func() {
		_, _ = m.Close()
	}()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		v, dirty, verr := m.Version()
		if verr != nil {
			return fmt.Errorf("migrate up: %w", err)
		}
		return fmt.Errorf("migrate up: version %d dirty=%v: %w", v, dirty, err)
	}
	return nil
}
