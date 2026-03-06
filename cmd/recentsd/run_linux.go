//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
	"recents/internal/config"
	"recents/internal/ingest"
	"recents/internal/storage"
)

const (
	inotifyMask = unix.IN_OPEN |
		unix.IN_CREATE |
		unix.IN_MOVED_TO |
		unix.IN_MOVED_FROM |
		unix.IN_DELETE_SELF |
		unix.IN_MOVE_SELF |
		unix.IN_IGNORED |
		unix.IN_Q_OVERFLOW |
		unix.IN_ONLYDIR
)

type linuxInotifyEvent struct {
	wd    int
	path  string
	mask  uint32
	isDir bool
}

type recursiveInotifyWatcher struct {
	fd       int
	wdToPath map[int]string
	pathToWd map[string]int
	ignored  []string
}

func run(ctx context.Context) error {
	cfg, err := config.Load("")
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	watcher, err := newRecursiveInotifyWatcher(cfg.IgnoredPaths)
	if err != nil {
		return fmt.Errorf("create inotify watcher: %w", err)
	}
	defer watcher.Close()

	for _, watchPath := range cfg.WatchPaths {
		if err := watcher.addRecursive(watchPath); err != nil {
			log.Printf("recentsd level=warn component=watcher action=init_watch root=%q err=%v", watchPath, err)
			if isWatchLimitErr(err) {
				logWatchLimitHint()
			}
		}
	}

	store, err := storage.Open(ctx, storage.Options{MaxEntries: cfg.MaxEntries})
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer store.Close()

	processor := ingest.NewProcessor(store, ingest.Options{
		DebounceWindow: ingest.DefaultDebounceWindow,
		MaxEntries:     cfg.MaxEntries,
	})
	filter := newFileFilter(cfg.TrackedExtensions, cfg.IgnoredPaths)

	eventQueue := make(chan ingest.Event, 2048)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case evt := <-eventQueue:
				if _, err := processor.Ingest(ctx, evt); err != nil {
					log.Printf("recentsd level=error component=ingest path=%q err=%v", evt.Path, err)
				}
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
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
		default:
		}

		events, err := watcher.readEvents()
		if err != nil {
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
				time.Sleep(25 * time.Millisecond)
				continue
			}
			log.Printf("recentsd level=warn component=watcher action=read err=%v", err)
			if isWatchLimitErr(err) {
				logWatchLimitHint()
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}

		if len(events) == 0 {
			time.Sleep(25 * time.Millisecond)
			continue
		}

		for _, ev := range events {
			if ev.mask&unix.IN_Q_OVERFLOW != 0 {
				log.Printf("recentsd level=warn component=watcher msg=%q", "inotify queue overflow; some events were dropped")
				continue
			}

			if ev.mask&unix.IN_IGNORED != 0 {
				watcher.removeByWD(ev.wd)
				continue
			}

			if ev.isDir && (ev.mask&unix.IN_CREATE != 0 || ev.mask&unix.IN_MOVED_TO != 0) {
				if err := watcher.addRecursive(ev.path); err != nil {
					log.Printf("recentsd level=warn component=watcher action=add_recursive path=%q err=%v", ev.path, err)
					if isWatchLimitErr(err) {
						logWatchLimitHint()
					}
				}
				continue
			}

			if ev.isDir && (ev.mask&unix.IN_MOVED_FROM != 0 || ev.mask&unix.IN_DELETE_SELF != 0 || ev.mask&unix.IN_MOVE_SELF != 0) {
				watcher.removeTree(ev.path)
				continue
			}

			if ev.mask&unix.IN_OPEN != 0 && !ev.isDir {
				if !shouldTrackFile(ev.path, filter) {
					continue
				}
				select {
				case eventQueue <- ingest.Event{Path: ev.path, OpenedAt: time.Now().UTC()}:
				default:
					log.Printf("recentsd level=warn component=queue action=drop path=%q reason=full", ev.path)
				}
			}
		}
	}
}

func newRecursiveInotifyWatcher(ignored []string) (*recursiveInotifyWatcher, error) {
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		return nil, err
	}
	return &recursiveInotifyWatcher{
		fd:       fd,
		wdToPath: make(map[int]string, 4096),
		pathToWd: make(map[string]int, 4096),
		ignored:  ignored,
	}, nil
}

func (w *recursiveInotifyWatcher) Close() error {
	return unix.Close(w.fd)
}

func (w *recursiveInotifyWatcher) addRecursive(root string) error {
	cleanRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return fmt.Errorf("normalize watch root %q: %w", root, err)
	}

	return filepath.WalkDir(cleanRoot, func(path string, d fs.DirEntry, err error) error {
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
		if isIgnoredPath(path, w.ignored) {
			return filepath.SkipDir
		}
		if err := w.addWatch(path); err != nil {
			if errors.Is(err, fs.ErrPermission) {
				log.Printf("recentsd level=warn component=watcher action=add path=%q reason=permission_denied", path)
				return filepath.SkipDir
			}
			if isWatchLimitErr(err) {
				return err
			}
			log.Printf("recentsd level=warn component=watcher action=add path=%q err=%v", path, err)
		}
		return nil
	})
}

func (w *recursiveInotifyWatcher) addWatch(path string) error {
	cleanPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	if _, exists := w.pathToWd[cleanPath]; exists {
		return nil
	}

	wd, err := unix.InotifyAddWatch(w.fd, cleanPath, inotifyMask)
	if err != nil {
		return err
	}

	w.wdToPath[wd] = cleanPath
	w.pathToWd[cleanPath] = wd
	return nil
}

func (w *recursiveInotifyWatcher) removeByWD(wd int) {
	path, ok := w.wdToPath[wd]
	if !ok {
		return
	}
	delete(w.wdToPath, wd)
	delete(w.pathToWd, path)
}

func (w *recursiveInotifyWatcher) removeTree(root string) {
	root = filepath.Clean(root)
	prefix := root + string(filepath.Separator)
	toRemove := make([]int, 0, 16)
	for path, wd := range w.pathToWd {
		if path == root || strings.HasPrefix(path, prefix) {
			toRemove = append(toRemove, wd)
		}
	}
	for _, wd := range toRemove {
		_, _ = unix.InotifyRmWatch(w.fd, uint32(wd))
		w.removeByWD(wd)
	}
}

func (w *recursiveInotifyWatcher) readEvents() ([]linuxInotifyEvent, error) {
	buf := make([]byte, 256*unix.SizeofInotifyEvent+4096)
	n, err := unix.Read(w.fd, buf)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}

	events := make([]linuxInotifyEvent, 0, 64)
	offset := 0
	for offset+unix.SizeofInotifyEvent <= n {
		raw := (*unix.InotifyEvent)(unsafe.Pointer(&buf[offset]))
		offset += unix.SizeofInotifyEvent

		name := ""
		if raw.Len > 0 {
			nameBytes := buf[offset : offset+int(raw.Len)]
			name = strings.TrimRight(string(nameBytes), "\x00")
			offset += int(raw.Len)
		}

		basePath := w.wdToPath[int(raw.Wd)]
		eventPath := basePath
		if name != "" {
			eventPath = filepath.Join(basePath, name)
		}

		events = append(events, linuxInotifyEvent{
			wd:    int(raw.Wd),
			path:  eventPath,
			mask:  raw.Mask,
			isDir: raw.Mask&unix.IN_ISDIR != 0,
		})
	}

	return events, nil
}
