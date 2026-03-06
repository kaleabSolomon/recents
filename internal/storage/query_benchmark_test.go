package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func BenchmarkListRecent(b *testing.B) {
	store, err := Open(context.Background(), Options{
		Path:       filepath.Join(b.TempDir(), "recents-bench.db"),
		MaxEntries: 50000,
	})
	if err != nil {
		b.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	for i := 0; i < 2000; i++ {
		_ = store.UpsertOpen(context.Background(), fmt.Sprintf("/tmp/bench-%d.txt", i), now.Add(time.Duration(i)*time.Second))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := store.ListRecent(context.Background(), QueryOptions{Limit: 200, Extensions: []string{"txt"}})
		if err != nil {
			b.Fatalf("ListRecent() error = %v", err)
		}
	}
}
