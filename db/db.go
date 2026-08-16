// Package db handles database connection setup and schema migrations.
// It is intentionally thin — just enough to wire the application to
// Postgres and apply pending migrations on startup. Storage logic lives
// in storage/postgres, business logic in wallet.
package db

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // postgres driver for migrate
	_ "github.com/golang-migrate/migrate/v4/source/file"       // file source for migration files
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq" // postgres driver for database/sql
)

// Connect opens a Postgres connection pool, verifies it with a ping,
// and configures sane pool defaults.
//
// Pool sizing rationale:
//   - MaxOpenConns 25: a reasonable upper bound for a single instance.
//     More connections than this rarely improves throughput and
//     increases memory and connection overhead on the database.
//   - MaxIdleConns 10: keep enough idle connections to handle bursts
//     without the overhead of opening a new connection each time.
//   - ConnMaxLifetime 5m: recycle connections periodically to avoid
//     issues with connections that have gone stale (e.g. after a
//     database failover or a NAT timeout).
func Connect(dsn string) (*sqlx.DB, error) {
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)
	return db, nil
}

// Migrate applies all pending up-migrations from migrationsPath.
// migrationsPath must use the file:// scheme and point to the directory
// containing the migration files (e.g. "file://migrations").
//
// ErrNoChange is treated as success — it means the schema is already
// up to date, which is the expected case on every restart after the
// first deployment. Any other error is returned wrapped with context.
func Migrate(dsn, migrationsPath string) error {
	m, err := migrate.New(migrationsPath, dsn)
	if err != nil {
		return fmt.Errorf("new migrator: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}