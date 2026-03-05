package ingest

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIngestDebouncesDuplicateEvents(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	p := NewProcessor(store, Options{
		DebounceWindow: 2 * time.Second,
		MaxEntries:     100,
		PruneEvery:     1,
	})

	base := time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC)
	recorded, err := p.Ingest(context.Background(), Event{
		Path:     "/tmp/file.txt",
		OpenedAt: base,
	})
	if err != nil {
		t.Fatalf("first ingest error = %v", err)
	}
	if !recorded {
		t.Fatalf("first ingest recorded = false, want true")
	}

	recorded, err = p.Ingest(context.Background(), Event{
		Path:     "/tmp/file.txt",
		OpenedAt: base.Add(1 * time.Second),
	})
	if err != nil {
		t.Fatalf("second ingest error = %v", err)
	}
	if recorded {
		t.Fatalf("second ingest recorded = true, want false (debounced)")
	}

	if len(store.upserts) != 1 {
		t.Fatalf("upsert calls = %d, want 1", len(store.upserts))
	}
	if store.pruneCalls != 1 {
		t.Fatalf("prune calls = %d, want 1", store.pruneCalls)
	}
}

func TestIngestRecordsAfterDebounceWindow(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	p := NewProcessor(store, Options{
		DebounceWindow: 2 * time.Second,
		MaxEntries:     100,
		PruneEvery:     1,
	})

	base := time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC)
	_, _ = p.Ingest(context.Background(), Event{Path: "/tmp/file.txt", OpenedAt: base})

	recorded, err := p.Ingest(context.Background(), Event{
		Path:     "/tmp/file.txt",
		OpenedAt: base.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatalf("third ingest error = %v", err)
	}
	if !recorded {
		t.Fatalf("third ingest recorded = false, want true")
	}

	if len(store.upserts) != 2 {
		t.Fatalf("upsert calls = %d, want 2", len(store.upserts))
	}
	if store.pruneCalls != 2 {
		t.Fatalf("prune calls = %d, want 2", store.pruneCalls)
	}
}

func TestIngestPropagatesStoreErrors(t *testing.T) {
	t.Parallel()

	upsertErr := errors.New("boom")
	store := &fakeStore{upsertErr: upsertErr}
	p := NewProcessor(store, Options{})

	_, err := p.Ingest(context.Background(), Event{Path: "/tmp/file.txt"})
	if err == nil {
		t.Fatalf("expected ingest error, got nil")
	}
}

func TestIngestPrunesOnConfiguredInterval(t *testing.T) {
	t.Parallel()

	store := &fakeStore{}
	p := NewProcessor(store, Options{
		DebounceWindow: 0,
		MaxEntries:     100,
		PruneEvery:     2,
	})

	base := time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC)
	_, _ = p.Ingest(context.Background(), Event{Path: "/tmp/a.txt", OpenedAt: base})
	_, _ = p.Ingest(context.Background(), Event{Path: "/tmp/b.txt", OpenedAt: base.Add(1 * time.Second)})
	_, _ = p.Ingest(context.Background(), Event{Path: "/tmp/c.txt", OpenedAt: base.Add(2 * time.Second)})

	if store.pruneCalls != 1 {
		t.Fatalf("prune calls = %d, want 1", store.pruneCalls)
	}
}

type fakeStore struct {
	upserts    []Event
	upsertErr  error
	pruneErr   error
	pruneCalls int
}

func (s *fakeStore) UpsertOpen(_ context.Context, path string, openedAt time.Time) error {
	if s.upsertErr != nil {
		return s.upsertErr
	}
	s.upserts = append(s.upserts, Event{Path: path, OpenedAt: openedAt})
	return nil
}

func (s *fakeStore) Prune(_ context.Context, _ int) (int64, error) {
	s.pruneCalls++
	if s.pruneErr != nil {
		return 0, s.pruneErr
	}
	return 0, nil
}
