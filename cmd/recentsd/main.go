package main

import (
	"context"
	"errors"
	"log"
	"os/signal"
	"strings"
	"syscall"
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

func isWatchLimitErr(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || strings.Contains(strings.ToLower(err.Error()), "no space left on device")
}

func logWatchLimitHint() {
	log.Printf("recentsd level=warn component=watcher msg=%q",
		"inotify watch limit reached (ENOSPC). Increase fs.inotify.max_user_watches and fs.inotify.max_user_instances or narrow watch_paths/ignored_paths")
}
