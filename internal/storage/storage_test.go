package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenInitializesSchema(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	defer func() { _ = store.Close() }()

	checkObjectExists(t, store, "table", "files")
	checkObjectExists(t, store, "table", "schema_migrations")
	checkObjectExists(t, store, "index", "idx_files_path")
	checkObjectExists(t, store, "index", "idx_files_last_opened")
	checkObjectExists(t, store, "index", "idx_files_extension")
}

func TestOpenRecordsMigrations(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	defer func() { _ = store.Close() }()

	var count int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count schema migrations: %v", err)
	}
	if count == 0 {
		t.Fatalf("expected at least one migration record, got %d", count)
	}
}

func TestPruneKeepsMostRecentRows(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	for i := 0; i < 6; i++ {
		ts := now.Add(time.Duration(i) * time.Minute)
		path := fmt.Sprintf("/tmp/file-%d.txt", i)
		if _, err := store.DB().Exec(`
INSERT INTO files(path, name, extension, last_opened, first_seen, open_count, directory)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
			path,
			fmt.Sprintf("file-%d.txt", i),
			"txt",
			ts,
			ts,
			i+1,
			"/tmp",
		); err != nil {
			t.Fatalf("insert row %d: %v", i, err)
		}
	}

	deleted, err := store.Prune(context.Background(), 3)
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if deleted != 3 {
		t.Fatalf("deleted rows = %d, want 3", deleted)
	}

	rows, err := store.DB().Query(`SELECT path FROM files ORDER BY last_opened DESC`)
	if err != nil {
		t.Fatalf("select remaining rows: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			t.Fatalf("scan remaining row: %v", err)
		}
		got = append(got, path)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate remaining rows: %v", err)
	}

	want := []string{"/tmp/file-5.txt", "/tmp/file-4.txt", "/tmp/file-3.txt"}
	if len(got) != len(want) {
		t.Fatalf("remaining row count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("remaining path[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestUpsertOpenIncrementsCountAndPreservesFirstSeen(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	defer func() { _ = store.Close() }()

	path := "/tmp/my-note.txt"
	first := time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC)
	second := first.Add(2 * time.Hour)

	if err := store.UpsertOpen(context.Background(), path, first); err != nil {
		t.Fatalf("first UpsertOpen() error = %v", err)
	}
	before := mustReadFileRow(t, store, path)
	if before.OpenCount != 1 {
		t.Fatalf("open_count after first upsert = %d, want 1", before.OpenCount)
	}
	if !before.FirstSeen.Equal(first) {
		t.Fatalf("first_seen after first upsert = %v, want %v", before.FirstSeen, first)
	}
	if !before.LastOpened.Equal(first) {
		t.Fatalf("last_opened after first upsert = %v, want %v", before.LastOpened, first)
	}

	if err := store.UpsertOpen(context.Background(), path, second); err != nil {
		t.Fatalf("second UpsertOpen() error = %v", err)
	}
	after := mustReadFileRow(t, store, path)
	if after.OpenCount != 2 {
		t.Fatalf("open_count after second upsert = %d, want 2", after.OpenCount)
	}
	if !after.FirstSeen.Equal(first) {
		t.Fatalf("first_seen after second upsert = %v, want %v", after.FirstSeen, first)
	}
	if !after.LastOpened.Equal(second) {
		t.Fatalf("last_opened after second upsert = %v, want %v", after.LastOpened, second)
	}
	if after.Directory != "/tmp" {
		t.Fatalf("directory = %q, want %q", after.Directory, "/tmp")
	}
	if after.Name != "my-note.txt" {
		t.Fatalf("name = %q, want %q", after.Name, "my-note.txt")
	}
	if after.Extension != "txt" {
		t.Fatalf("extension = %q, want %q", after.Extension, "txt")
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()

	path := filepath.Join(t.TempDir(), "recents-test.db")
	store, err := Open(context.Background(), Options{
		Path:       path,
		MaxEntries: 50000,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return store
}

func checkObjectExists(t *testing.T, store *Store, objectType, name string) {
	t.Helper()

	var found string
	err := store.DB().QueryRow(`
SELECT name
FROM sqlite_master
WHERE type = ? AND name = ?`,
		objectType, name,
	).Scan(&found)
	if err != nil {
		t.Fatalf("query sqlite_master for %s %q: %v", objectType, name, err)
	}
}

type fileRow struct {
	Name       string
	Extension  string
	LastOpened time.Time
	FirstSeen  time.Time
	OpenCount  int
	Directory  string
}

func mustReadFileRow(t *testing.T, store *Store, path string) fileRow {
	t.Helper()

	absPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		t.Fatalf("normalize path %q: %v", path, err)
	}

	var row fileRow
	err = store.DB().QueryRow(`
SELECT name, extension, last_opened, first_seen, open_count, directory
FROM files
WHERE path = ?`, absPath).Scan(
		&row.Name,
		&row.Extension,
		&row.LastOpened,
		&row.FirstSeen,
		&row.OpenCount,
		&row.Directory,
	)
	if err != nil {
		t.Fatalf("query row for path %q: %v", path, err)
	}
	return row
}
