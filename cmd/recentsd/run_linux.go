//go:build linux

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
	"recents/internal/config"
	"recents/internal/ingest"
	"recents/internal/storage"
)

type fanOpenEvent struct {
	path string
	pid  int
}

type fanotifyWatcher struct {
	fd          int
	watchRoots  []string
	mountPoints []string
}

func run(ctx context.Context) error {
	cfg, err := config.Load("")
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	roots, err := normalizeWatchRoots(cfg.WatchPaths)
	if err != nil {
		return err
	}

	watcher, err := newFanotifyWatcher(roots)
	if err != nil {
		if errors.Is(err, syscall.EPERM) {
			return fmt.Errorf("fanotify requires elevated privileges (run recentsd with sudo/root): %w", err)
		}
		return fmt.Errorf("create fanotify watcher: %w", err)
	}
	defer watcher.Close()

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
	// Tracked opens are held for the gate's window so that mass-open bursts
	// (indexers, thumbnailers, app startup scans) can be dropped in full.
	gate := newOpenGate(2*time.Second, 3, 5*time.Second)

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

	enqueue := func(released []gateEvent) {
		for _, ev := range released {
			select {
			case eventQueue <- ingest.Event{Path: ev.path, OpenedAt: ev.at}:
			default:
				log.Printf("recentsd level=warn component=queue action=drop path=%q reason=full", ev.path)
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			// Release any held opens directly so the user's last actions are
			// not lost; the queue worker is exiting on ctx.Done.
			shutdownCtx, cancelIngest := context.WithTimeout(context.Background(), 2*time.Second)
			for _, ev := range gate.FlushAll() {
				if _, err := processor.Ingest(shutdownCtx, ingest.Event{Path: ev.path, OpenedAt: ev.at}); err != nil {
					log.Printf("recentsd level=warn component=ingest action=final_flush path=%q err=%v", ev.path, err)
				}
			}
			cancelIngest()

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
				enqueue(gate.Flush(time.Now().UTC()))
				time.Sleep(20 * time.Millisecond)
				continue
			}
			log.Printf("recentsd level=warn component=watcher action=read err=%v", err)
			if isWatchLimitErr(err) {
				logWatchLimitHint()
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}

		now := time.Now().UTC()
		for _, ev := range events {
			// Cheap path checks first; the /proc attribution checks run only
			// for files that would actually be recorded, so app startup noise
			// (dotfiles, caches, fonts) never reaches the burst gate.
			if !pathUnderRoots(ev.path, roots) {
				continue
			}
			if !shouldTrackFile(ev.path, filter) {
				continue
			}
			if !isLikelyUserInitiatedOpen(ev.pid) {
				continue
			}
			gate.Add(ev.pid, ev.path, now)
		}
		enqueue(gate.Flush(now))
	}
}

func newFanotifyWatcher(watchRoots []string) (*fanotifyWatcher, error) {
	fd, err := unix.FanotifyInit(
		unix.FAN_CLASS_NOTIF|unix.FAN_CLOEXEC|unix.FAN_NONBLOCK|unix.FAN_UNLIMITED_MARKS|unix.FAN_UNLIMITED_QUEUE,
		unix.O_RDONLY|unix.O_LARGEFILE,
	)
	if err != nil {
		return nil, err
	}

	mounts, err := mountPointsForRoots(watchRoots)
	if err != nil {
		_ = unix.Close(fd)
		return nil, err
	}

	for _, mp := range mounts {
		err := unix.FanotifyMark(fd, unix.FAN_MARK_ADD|unix.FAN_MARK_MOUNT, unix.FAN_OPEN, unix.AT_FDCWD, mp)
		if err != nil {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("fanotify mark %q: %w", mp, err)
		}
	}

	return &fanotifyWatcher{fd: fd, watchRoots: watchRoots, mountPoints: mounts}, nil
}

func (w *fanotifyWatcher) Close() error {
	return unix.Close(w.fd)
}

func (w *fanotifyWatcher) readEvents() ([]fanOpenEvent, error) {
	buf := make([]byte, 64*1024)
	n, err := unix.Read(w.fd, buf)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}

	events := make([]fanOpenEvent, 0, 64)
	metaSize := int(unsafe.Sizeof(unix.FanotifyEventMetadata{}))
	offset := 0
	for offset+metaSize <= n {
		meta := (*unix.FanotifyEventMetadata)(unsafe.Pointer(&buf[offset]))
		if meta.Event_len < uint32(metaSize) {
			break
		}

		eventLen := int(meta.Event_len)
		if meta.Vers != unix.FANOTIFY_METADATA_VERSION {
			offset += eventLen
			continue
		}

		if meta.Mask&unix.FAN_Q_OVERFLOW != 0 {
			log.Printf("recentsd level=warn component=watcher msg=%q", "fanotify queue overflow; some events were dropped")
			offset += eventLen
			continue
		}

		if meta.Fd >= 0 {
			if meta.Mask&unix.FAN_OPEN != 0 {
				path, pathErr := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", meta.Fd))
				if pathErr == nil && path != "" {
					events = append(events, fanOpenEvent{path: filepath.Clean(path), pid: int(meta.Pid)})
				}
			}
			// Always close the event fd, even for masks we don't handle.
			_ = unix.Close(int(meta.Fd))
		}

		offset += eventLen
	}

	return events, nil
}

func normalizeWatchRoots(paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		abs, err := filepath.Abs(filepath.Clean(p))
		if err != nil {
			return nil, fmt.Errorf("normalize watch path %q: %w", p, err)
		}
		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		out = append(out, abs)
	}
	return out, nil
}

func pathUnderRoots(path string, roots []string) bool {
	path = filepath.Clean(path)
	sep := string(filepath.Separator)
	for _, root := range roots {
		if path == root || strings.HasPrefix(path, root+sep) {
			return true
		}
	}
	return false
}

func isLikelyUserInitiatedOpen(pid int) bool {
	if pid <= 1 || pid == os.Getpid() {
		return false
	}

	procPath := fmt.Sprintf("/proc/%d", pid)
	st, err := os.Stat(procPath)
	if err != nil {
		return false
	}
	statT, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	if int(statT.Uid) != targetUID() {
		return false
	}

	commBytes, err := os.ReadFile(filepath.Join(procPath, "comm"))
	if err != nil {
		return false
	}
	comm := strings.TrimSpace(strings.ToLower(string(commBytes)))
	if comm == "" {
		return false
	}
	if _, blocked := backgroundProcessBlocklist[comm]; blocked {
		return false
	}

	// Keep processes tied to interactive shells/terminals.
	if hasTTY(pid) {
		return true
	}
	// Keep GUI apps from current session.
	if hasSessionDisplayEnv(pid) {
		return true
	}

	return false
}

func targetUID() int {
	// When running via sudo, still attribute opens to the invoking user.
	if sudoUID := strings.TrimSpace(os.Getenv("SUDO_UID")); sudoUID != "" {
		if uid, err := strconv.Atoi(sudoUID); err == nil {
			return uid
		}
	}
	return os.Getuid()
}

// commMaxLen is the kernel's TASK_COMM_LEN minus the NUL terminator:
// /proc/PID/comm is truncated to at most 15 characters.
const commMaxLen = 15

func truncateComm(s string) string {
	if len(s) > commMaxLen {
		return s[:commMaxLen]
	}
	return s
}

// backgroundProcessBlocklist holds comm names (truncated to the kernel's
// 15-char limit) of indexers, thumbnailers, and other background scanners
// whose opens must never be recorded.
var backgroundProcessBlocklist = func() map[string]struct{} {
	names := []string{
		"recentsd",
		// Indexers
		"tracker-miner-fs-3",
		"tracker-extract-3",
		"localsearch-3",
		"baloo_file",
		"baloo_file_extractor",
		"updatedb",
		"plocate-build",
		"locate",
		"gvfsd-metadata",
		// Thumbnailers
		"tumblerd",
		"evince-thumbnailer",
		"totem-video-thumbnailer",
		"ffmpegthumbnailer",
		"gdk-pixbuf-thumbnailer",
		"gnome-desktop-thumbnailer",
		// Device sync daemons
		"kdeconnectd",
		"kio-fuse",
	}
	m := make(map[string]struct{}, len(names))
	for _, n := range names {
		m[truncateComm(n)] = struct{}{}
	}
	return m
}()

func hasTTY(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	line := string(data)
	end := strings.LastIndex(line, ")")
	if end == -1 || end+2 >= len(line) {
		return false
	}
	fields := strings.Fields(line[end+2:])
	if len(fields) < 5 {
		return false
	}
	ttyNR, err := strconv.Atoi(fields[4])
	if err != nil {
		return false
	}
	return ttyNR != 0
}

func hasSessionDisplayEnv(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return false
	}
	env := string(data)
	return strings.Contains(env, "DISPLAY=") || strings.Contains(env, "WAYLAND_DISPLAY=")
}

func mountPointsForRoots(roots []string) ([]string, error) {
	mounts, err := listMountPoints()
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})
	result := make([]string, 0, len(roots))
	for _, root := range roots {
		mp := bestMountPoint(root, mounts)
		if mp == "" {
			continue
		}
		if _, ok := seen[mp]; ok {
			continue
		}
		seen[mp] = struct{}{}
		result = append(result, mp)
	}
	if len(result) == 0 {
		return nil, errors.New("no mount points resolved for watch roots")
	}
	return result, nil
}

func listMountPoints() ([]string, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("open mountinfo: %w", err)
	}
	defer f.Close()

	// mountinfo octal-escapes space, tab, newline, and backslash.
	replacer := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	result := make([]string, 0, 64)
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		left := strings.SplitN(line, " - ", 2)[0]
		fields := strings.Fields(left)
		if len(fields) < 5 {
			continue
		}
		mp := filepath.Clean(replacer.Replace(fields[4]))
		result = append(result, mp)
	}
	if err := s.Err(); err != nil {
		return nil, fmt.Errorf("scan mountinfo: %w", err)
	}
	return result, nil
}

func bestMountPoint(path string, mounts []string) string {
	path = filepath.Clean(path)
	best := ""
	for _, mp := range mounts {
		if path == mp {
			if len(mp) > len(best) {
				best = mp
			}
			continue
		}
		if mp == string(filepath.Separator) {
			// Root mount is a parent of all absolute paths.
			if strings.HasPrefix(path, string(filepath.Separator)) && len(mp) > len(best) {
				best = mp
			}
			continue
		}
		if strings.HasPrefix(path, mp+string(filepath.Separator)) {
			if len(mp) > len(best) {
				best = mp
			}
		}
	}
	return best
}
