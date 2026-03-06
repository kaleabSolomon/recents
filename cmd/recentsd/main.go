package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
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

	watched := make(map[string]struct{}, 4096)
	for _, watchPath := range cfg.WatchPaths {
		if err := addRecursiveWatches(watcher, watched, watchPath, cfg.IgnoredPaths); err != nil {
			log.Printf("recentsd level=warn component=watcher action=init_watch root=%q err=%v", watchPath, err)
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
	filter := newFileFilter(cfg.TrackedExtensions, cfg.IgnoredPaths)

	eventQueue := make(chan ingest.Event, 1024)
	for _, watchPath := range watcher.WatchList() {
		watched[watchPath] = struct{}{}
	}

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-eventQueue:
				if !ok {
					return
				}
				if _, err := processor.Ingest(ctx, evt); err != nil {
					log.Printf("recentsd level=error component=ingest path=%q err=%v", evt.Path, err)
					select {
					case <-ctx.Done():
						return
					case <-time.After(200 * time.Millisecond):
						if _, retryErr := processor.Ingest(ctx, evt); retryErr != nil {
							log.Printf("recentsd level=error component=ingest path=%q retry=true err=%v", evt.Path, retryErr)
						}
					}
				}
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("recentsd level=warn component=watcher err=%v", err)
				for _, root := range cfg.WatchPaths {
					if rewatchErr := addRecursiveWatches(watcher, watched, root, cfg.IgnoredPaths); rewatchErr != nil {
						log.Printf("recentsd level=warn component=watcher action=rewatch root=%q err=%v", root, rewatchErr)
					}
				}
			case evt, ok := <-watcher.Events:
				if !ok {
					return
				}
				handleDirectoryWatchLifecycle(watcher, watched, evt, cfg.IgnoredPaths)

				if !shouldIngestEvent(evt) || !shouldTrackFile(evt.Name, filter) {
					continue
				}
				select {
				case eventQueue <- ingest.Event{Path: evt.Name, OpenedAt: time.Now().UTC()}:
				default:
					log.Printf("recentsd level=warn component=queue action=drop path=%q reason=full", evt.Name)
				}
			}
		}
	}()

	<-ctx.Done()

	// Give worker goroutines a short window to flush and exit cleanly.
	waitDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		log.Printf("recentsd level=warn component=shutdown msg=%q", "timed out waiting for workers")
	}

	pruneCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if deleted, err := store.Prune(pruneCtx, cfg.MaxEntries); err != nil {
		log.Printf("recentsd level=warn component=storage action=final_prune err=%v", err)
	} else if deleted > 0 {
		log.Printf("recentsd level=info component=storage action=final_prune deleted=%d", deleted)
	}

	return ctx.Err()
}

func shouldIngestEvent(evt fsnotify.Event) bool {
	return evt.Has(fsnotify.Write) || evt.Has(fsnotify.Create)
}

func addRecursiveWatches(watcher *fsnotify.Watcher, watched map[string]struct{}, root string, ignored []string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				log.Printf("recentsd level=warn component=watcher action=walk path=%q reason=permission_denied", path)
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if isIgnoredPath(path, ignored) {
			return filepath.SkipDir
		}
		if _, ok := watched[path]; ok {
			return nil
		}
		if err := watcher.Add(path); err != nil {
			if errors.Is(err, fs.ErrPermission) {
				log.Printf("recentsd level=warn component=watcher action=add path=%q reason=permission_denied", path)
				return filepath.SkipDir
			}
			log.Printf("recentsd level=warn component=watcher action=add path=%q err=%v", path, err)
			return nil
		}
		watched[path] = struct{}{}
		return nil
	})
}

func handleDirectoryWatchLifecycle(watcher *fsnotify.Watcher, watched map[string]struct{}, evt fsnotify.Event, ignored []string) {
	path := filepath.Clean(evt.Name)

	if evt.Has(fsnotify.Remove) || evt.Has(fsnotify.Rename) {
		for watchedPath := range watched {
			if watchedPath == path || strings.HasPrefix(watchedPath, path+string(filepath.Separator)) {
				_ = watcher.Remove(watchedPath)
				delete(watched, watchedPath)
			}
		}
		return
	}

	if !evt.Has(fsnotify.Create) {
		return
	}

	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return
	}

	if err := addRecursiveWatches(watcher, watched, path, ignored); err != nil {
		log.Printf("recentsd level=warn component=watcher action=add_recursive path=%q err=%v", path, err)
	}
}
