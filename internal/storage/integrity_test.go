package storage

import (
	"context"
	"testing"
	"time"
)

func TestSQLiteIntegrityCheck(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	defer func() { _ = store.Close() }()

	var result string
	if err := store.DB().QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		t.Fatalf("integrity_check query failed: %v", err)
	}
	if result != "ok" {
		t.Fatalf("integrity_check = %q, want %q", result, "ok")
	}
}

func TestLatestMigrationApplied(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	defer func() { _ = store.Close() }()

	checkObjectExists(t, store, "index", "idx_files_extension_last_opened")
}

func TestPruneNoopWhenUnderLimit(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	if err := store.UpsertOpen(context.Background(), "/tmp/keep-1.txt", now); err != nil {
		t.Fatalf("upsert keep-1: %v", err)
	}
	if err := store.UpsertOpen(context.Background(), "/tmp/keep-2.txt", now.Add(time.Second)); err != nil {
		t.Fatalf("upsert keep-2: %v", err)
	}

	deleted, err := store.Prune(context.Background(), 10)
	if err != nil {
		t.Fatalf("prune under limit: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("deleted rows = %d, want 0", deleted)
	}
}
