package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestListRecentSortsByLastOpened(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	defer func() { _ = store.Close() }()

	now := time.Date(2026, 3, 6, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		path := fmt.Sprintf("/tmp/sort-%d.txt", i)
		ts := now.Add(time.Duration(i) * time.Minute)
		if err := store.UpsertOpen(context.Background(), path, ts); err != nil {
			t.Fatalf("UpsertOpen(%q) error = %v", path, err)
		}
	}

	recs, err := store.ListRecent(context.Background(), QueryOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListRecent() error = %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("len(records) = %d, want 3", len(recs))
	}
	if recs[0].Path != filepath.Clean("/tmp/sort-2.txt") {
		t.Fatalf("first path = %q, want %q", recs[0].Path, filepath.Clean("/tmp/sort-2.txt"))
	}
}

func TestListRecentSearchAndFilter(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	defer func() { _ = store.Close() }()

	now := time.Date(2026, 3, 6, 0, 0, 0, 0, time.UTC)
	_ = store.UpsertOpen(context.Background(), "/tmp/MyDoc.txt", now)
	_ = store.UpsertOpen(context.Background(), "/tmp/other.go", now.Add(1*time.Minute))
	_ = store.UpsertOpen(context.Background(), "/tmp/notes.txt", now.Add(2*time.Minute))

	recs, err := store.ListRecent(context.Background(), QueryOptions{
		Search:     "doc",
		Extensions: []string{"txt"},
		Limit:      20,
	})
	if err != nil {
		t.Fatalf("ListRecent() error = %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("len(records) = %d, want 1", len(recs))
	}
	if recs[0].Name != "MyDoc.txt" {
		t.Fatalf("name = %q, want %q", recs[0].Name, "MyDoc.txt")
	}
}

func TestFormatHomePath(t *testing.T) {
	t.Parallel()

	orig := getUserHomeDir
	getUserHomeDir = func() string { return "/home/tester" }
	t.Cleanup(func() { getUserHomeDir = orig })

	if got := FormatHomePath("/home/tester/work/file.txt"); got != "~/work/file.txt" {
		t.Fatalf("FormatHomePath() = %q, want %q", got, "~/work/file.txt")
	}
	if got := FormatHomePath("/opt/file.txt"); got != "/opt/file.txt" {
		t.Fatalf("FormatHomePath() = %q, want %q", got, "/opt/file.txt")
	}
}
