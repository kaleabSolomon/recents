package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
	"recents/internal/defaults"
	"recents/internal/identity"
)

type Options struct {
	Path       string
	MaxEntries int
}

type Store struct {
	db         *sql.DB
	maxEntries int
}

func DefaultPath() (string, error) {
	home, err := identity.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "recents", "recents.db"), nil
}

func Open(ctx context.Context, opts Options) (*Store, error) {
	path := opts.Path
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set journal mode: %w", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA synchronous=NORMAL`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set synchronous mode: %w", err)
	}

	if err := applyMigrations(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}

	maxEntries := opts.MaxEntries
	if maxEntries <= 0 {
		maxEntries = defaults.MaxEntries
	}

	return &Store{db: db, maxEntries: maxEntries}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) UpsertOpen(ctx context.Context, path string, openedAt time.Time) error {
	cleanPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("normalize path %q: %w", path, err)
	}

	name := filepath.Base(cleanPath)
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(cleanPath)), ".")
	directory := filepath.Dir(cleanPath)
	if openedAt.IsZero() {
		openedAt = time.Now().UTC()
	}

	_, err = s.db.ExecContext(ctx, `
INSERT INTO files(path, name, extension, last_opened, first_seen, open_count, directory)
VALUES (?, ?, ?, ?, ?, 1, ?)
ON CONFLICT(path) DO UPDATE SET
	name = excluded.name,
	extension = excluded.extension,
	last_opened = excluded.last_opened,
	open_count = files.open_count + 1,
	directory = excluded.directory
	-- first_seen intentionally omitted to preserve the original value
`, cleanPath, name, ext, openedAt.UTC(), openedAt.UTC(), directory)
	if err != nil {
		return fmt.Errorf("upsert file open for %q: %w", cleanPath, err)
	}

	return nil
}

func (s *Store) Prune(ctx context.Context, maxEntries int) (int64, error) {
	if maxEntries <= 0 {
		maxEntries = s.maxEntries
	}

	result, err := s.db.ExecContext(ctx, `
DELETE FROM files
WHERE id IN (
	SELECT id
	FROM files
	ORDER BY last_opened DESC, id DESC
	LIMIT -1 OFFSET ?
)`, maxEntries)
	if err != nil {
		return 0, fmt.Errorf("prune files: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}

	return affected, nil
}

type migration struct {
	version int
	name    string
	apply   func(context.Context, *sql.Tx) error
}

var migrations = []migration{
	{
		version: 1,
		name:    "create files table and indexes",
		apply: func(ctx context.Context, tx *sql.Tx) error {
			statements := []string{
				`CREATE TABLE IF NOT EXISTS files (
					id INTEGER PRIMARY KEY,
					path TEXT NOT NULL UNIQUE,
					name TEXT NOT NULL,
					extension TEXT NOT NULL,
					last_opened DATETIME NOT NULL,
					first_seen DATETIME NOT NULL,
					open_count INTEGER NOT NULL DEFAULT 0,
					directory TEXT NOT NULL
				)`,
				`CREATE INDEX IF NOT EXISTS idx_files_path ON files(path)`,
				`CREATE INDEX IF NOT EXISTS idx_files_last_opened ON files(last_opened DESC)`,
				`CREATE INDEX IF NOT EXISTS idx_files_extension ON files(extension)`,
			}
			for _, stmt := range statements {
				if _, err := tx.ExecContext(ctx, stmt); err != nil {
					return err
				}
			}
			return nil
		},
	},
	{
		version: 2,
		name:    "add optimized query index for extension and recency",
		apply: func(ctx context.Context, tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_files_extension_last_opened ON files(extension, last_opened DESC)`); err != nil {
				return err
			}
			return nil
		},
	},
}

func applyMigrations(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
	version INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	applied_at DATETIME NOT NULL
)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied := make(map[int]struct{}, len(migrations))
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("list schema_migrations: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return fmt.Errorf("scan schema migration version: %w", err)
		}
		applied[version] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate schema migrations: %w", err)
	}

	for _, m := range migrations {
		if _, ok := applied[m.version]; ok {
			continue
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", m.version, err)
		}

		if err := m.apply(ctx, tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %d (%s): %w", m.version, m.name, err)
		}

		if _, err := tx.ExecContext(ctx, `
INSERT INTO schema_migrations (version, name, applied_at)
VALUES (?, ?, ?)`,
			m.version, m.name, time.Now().UTC()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %d (%s): %w", m.version, m.name, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", m.version, err)
		}
	}

	return nil
}
