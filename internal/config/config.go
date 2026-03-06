package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"recents/internal/defaults"
)

const DefaultMaxEntries = defaults.MaxEntries

type Config struct {
	WatchPaths        []string `toml:"watch_paths"`
	IgnoredPaths      []string `toml:"ignored_paths"`
	TrackedExtensions []string `toml:"tracked_extensions"`
	MaxEntries        int      `toml:"max_entries"`
}

func Default() Config {
	return Config{
		WatchPaths: []string{"~/"},
		IgnoredPaths: []string{
			"node_modules",
			".git",
			".cache",
			"Downloads/tmp",
			"go/pkg/mod",
			".cargo",
			".npm",
			".pnpm-store",
			".local/share/nvim",
			".config/Code",
		},
		TrackedExtensions: []string{
			"mkv", "mp4", "avi", "mov", "webm",
			"mp3", "flac", "wav", "ogg",
			"pdf", "epub", "docx", "txt", "md",
		},
		MaxEntries: DefaultMaxEntries,
	}
}

func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".config", "recents", "config.toml"), nil
}

func Load(path string) (Config, error) {
	cfg := Default()

	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return Config{}, err
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return normalize(cfg)
		}
		return Config{}, fmt.Errorf("read config file %q: %w", path, err)
	}

	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config file %q: %w", path, err)
	}

	return normalize(cfg)
}

func normalize(cfg Config) (Config, error) {
	watchPaths := make([]string, 0, len(cfg.WatchPaths))
	for _, p := range cfg.WatchPaths {
		norm, err := normalizePath(p)
		if err != nil {
			return Config{}, fmt.Errorf("normalize watch path %q: %w", p, err)
		}
		watchPaths = append(watchPaths, norm)
	}

	ignoredPaths := make([]string, 0, len(cfg.IgnoredPaths))
	for _, p := range cfg.IgnoredPaths {
		norm, err := normalizeIgnoredPath(p)
		if err != nil {
			return Config{}, fmt.Errorf("normalize ignored path %q: %w", p, err)
		}
		ignoredPaths = append(ignoredPaths, norm)
	}

	cfg.WatchPaths = dedupeAndSort(watchPaths)
	cfg.IgnoredPaths = dedupeAndSort(ignoredPaths)
	cfg.TrackedExtensions = normalizeExtensions(cfg.TrackedExtensions)
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = DefaultMaxEntries
	}

	if len(cfg.WatchPaths) == 0 {
		fallback, err := normalizePath("~/")
		if err != nil {
			return Config{}, err
		}
		cfg.WatchPaths = []string{fallback}
	}

	if len(cfg.TrackedExtensions) == 0 {
		cfg.TrackedExtensions = Default().TrackedExtensions
	}

	return cfg, nil
}

func normalizePath(p string) (string, error) {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return "", nil
	}

	if strings.HasPrefix(trimmed, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		trimmed = filepath.Join(home, strings.TrimPrefix(trimmed, "~"))
	}

	clean := filepath.Clean(trimmed)
	if filepath.IsAbs(clean) {
		return clean, nil
	}

	abs, err := filepath.Abs(clean)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}
	return abs, nil
}

func normalizeIgnoredPath(p string) (string, error) {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return "", nil
	}

	if strings.HasPrefix(trimmed, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		trimmed = filepath.Join(home, strings.TrimPrefix(trimmed, "~"))
	}

	return filepath.Clean(trimmed), nil
}

func normalizeExtensions(exts []string) []string {
	result := make([]string, 0, len(exts))
	for _, e := range exts {
		e = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(e), "."))
		if e == "" {
			continue
		}
		result = append(result, e)
	}
	return dedupeAndSort(result)
}

func dedupeAndSort(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		result = append(result, v)
	}
	sort.Strings(result)
	return result
}
