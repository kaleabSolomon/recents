package ingest

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"recents/internal/defaults"
)

const DefaultDebounceWindow = 2 * time.Second
const defaultPruneEvery = 500

type Store interface {
	UpsertOpen(ctx context.Context, path string, openedAt time.Time) error
	Prune(ctx context.Context, maxEntries int) (int64, error)
}

type Event struct {
	Path     string
	OpenedAt time.Time
}

type Options struct {
	DebounceWindow time.Duration
	MaxEntries     int
	PruneEvery     int
}

type Processor struct {
	store          Store
	debounceWindow time.Duration
	maxEntries     int
	pruneEvery     int

	mu       sync.Mutex
	lastSeen map[string]time.Time
	count    int
}

func NewProcessor(store Store, opts Options) *Processor {
	window := opts.DebounceWindow
	if window <= 0 {
		window = DefaultDebounceWindow
	}
	maxEntries := opts.MaxEntries
	if maxEntries <= 0 {
		maxEntries = defaults.MaxEntries
	}
	pruneEvery := opts.PruneEvery
	if pruneEvery <= 0 {
		pruneEvery = defaultPruneEvery
	}

	return &Processor{
		store:          store,
		debounceWindow: window,
		maxEntries:     maxEntries,
		pruneEvery:     pruneEvery,
		lastSeen:       make(map[string]time.Time, 1024),
	}
}

func (p *Processor) Ingest(ctx context.Context, evt Event) (bool, error) {
	if evt.Path == "" {
		return false, nil
	}

	cleanPath, err := filepath.Abs(filepath.Clean(evt.Path))
	if err != nil {
		return false, fmt.Errorf("normalize event path %q: %w", evt.Path, err)
	}

	openedAt := evt.OpenedAt
	if openedAt.IsZero() {
		openedAt = time.Now().UTC()
	}
	openedAt = openedAt.UTC()

	if p.shouldSkip(cleanPath, openedAt) {
		return false, nil
	}

	if err := p.store.UpsertOpen(ctx, cleanPath, openedAt); err != nil {
		return false, fmt.Errorf("upsert open event: %w", err)
	}

	if p.shouldPrune() {
		if _, err := p.store.Prune(ctx, p.maxEntries); err != nil {
			return false, fmt.Errorf("prune store: %w", err)
		}
	}

	return true, nil
}

func (p *Processor) shouldSkip(path string, openedAt time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	last, ok := p.lastSeen[path]
	if ok {
		delta := openedAt.Sub(last)
		if delta >= 0 && delta < p.debounceWindow {
			return true
		}
	}

	p.lastSeen[path] = openedAt
	if len(p.lastSeen) > 10_000 {
		p.evictBeforeLocked(openedAt.Add(-p.debounceWindow))
	}
	return false
}

func (p *Processor) shouldPrune() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.count++
	return p.count%p.pruneEvery == 0
}

func (p *Processor) evictBeforeLocked(cutoff time.Time) {
	for path, ts := range p.lastSeen {
		if ts.Before(cutoff) {
			delete(p.lastSeen, path)
		}
	}
}
