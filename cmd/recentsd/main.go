package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"recents/internal/config"
	"recents/internal/ingest"
	"recents/internal/storage"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		log.Fatalf("recentsd: %v", err)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.Load("")
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create watcher: %w", err)
	}
	defer watcher.Close()

	for _, watchPath := range cfg.WatchPaths {
		if err := addRecursiveWatches(watcher, watchPath); err != nil {
			log.Printf("recentsd: add watch path %q: %v", watchPath, err)
		}
	}

	store, err := storage.Open(ctx, storage.Options{
		MaxEntries: cfg.MaxEntries,
	})
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer store.Close()

	processor := ingest.NewProcessor(store, ingest.Options{
		DebounceWindow: ingest.DefaultDebounceWindow,
		MaxEntries:     cfg.MaxEntries,
	})

	eventQueue := make(chan ingest.Event, 1024)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-eventQueue:
				if !ok {
					return
				}
				if _, err := processor.Ingest(ctx, evt); err != nil {
					log.Printf("recentsd: ingest error: %v", err)
				}
			}
		}
	}()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("recentsd: watcher error: %v", err)
			case evt, ok := <-watcher.Events:
				if !ok {
					return
				}
				if !shouldIngestEvent(evt) {
					continue
				}
				select {
				case eventQueue <- ingest.Event{Path: evt.Name, OpenedAt: time.Now().UTC()}:
				default:
					log.Printf("recentsd: event queue full, dropping %q", evt.Name)
				}
			}
		}
	}()

	<-ctx.Done()
	return ctx.Err()
}

func shouldIngestEvent(evt fsnotify.Event) bool {
	return evt.Has(fsnotify.Write) || evt.Has(fsnotify.Create)
}

func addRecursiveWatches(watcher *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if err := watcher.Add(path); err != nil {
			log.Printf("recentsd: watch add failed for %q: %v", path, err)
		}
		return nil
	})
}
