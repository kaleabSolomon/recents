package ingest

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"recents/internal/storage"
)

func TestProcessorIngestIntegrationWithSQLite(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "recents-integration.db")
	store, err := storage.Open(context.Background(), storage.Options{
		Path:       dbPath,
		MaxEntries: 3,
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	p := NewProcessor(store, Options{
		DebounceWindow: 2 * time.Second,
		MaxEntries:     3,
		PruneEvery:     1,
	})

	base := time.Date(2026, 3, 6, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{Path: "/tmp/a.txt", OpenedAt: base},
		{Path: "/tmp/a.txt", OpenedAt: base.Add(1 * time.Second)}, // debounced
		{Path: "/tmp/b.txt", OpenedAt: base.Add(2 * time.Second)},
		{Path: "/tmp/c.txt", OpenedAt: base.Add(3 * time.Second)},
		{Path: "/tmp/d.txt", OpenedAt: base.Add(4 * time.Second)}, // triggers prune to max=3
	}

	for _, evt := range events {
		if _, err := p.Ingest(context.Background(), evt); err != nil {
			t.Fatalf("ingest %q: %v", evt.Path, err)
		}
	}

	var rows int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM files`).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 3 {
		t.Fatalf("rows = %d, want 3", rows)
	}

	var openCount int
	if err := store.DB().QueryRow(`SELECT open_count FROM files WHERE path = ?`, "/tmp/a.txt").Scan(&openCount); err == nil {
		// /tmp/a.txt is expected to be pruned by recency once d.txt is inserted.
		t.Fatalf("expected /tmp/a.txt to be pruned, but found open_count=%d", openCount)
	} else if err != sql.ErrNoRows {
		t.Fatalf("query /tmp/a.txt: %v", err)
	}
}
