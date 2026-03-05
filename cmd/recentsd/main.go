package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/signal"
	"syscall"

	"github.com/fsnotify/fsnotify"
	"recents/internal/config"
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
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create watcher: %w", err)
	}
	defer watcher.Close()

	store, err := storage.Open(ctx, storage.Options{
		MaxEntries: config.DefaultMaxEntries,
	})
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer store.Close()

	<-ctx.Done()
	return ctx.Err()
}
