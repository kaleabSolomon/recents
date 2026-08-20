package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLoadDefaultsWhenConfigMissing(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "missing.toml")
	cfg, err := Load(missing)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() error = %v", err)
	}

	if got, want := cfg.MaxEntries, DefaultMaxEntries; got != want {
		t.Fatalf("MaxEntries = %d, want %d", got, want)
	}

	if len(cfg.WatchPaths) != 1 {
		t.Fatalf("WatchPaths length = %d, want 1", len(cfg.WatchPaths))
	}
	if got, want := cfg.WatchPaths[0], filepath.Clean(home); got != want {
		t.Fatalf("WatchPaths[0] = %q, want %q", got, want)
	}
}

func TestLoadNormalizesConfigFields(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	cfgPath := filepath.Join(tempDir, "config.toml")
	content := `watch_paths = ["~/work", "./rel", "~/work"]
ignored_paths = ["node_modules", " ./tmp ", "node_modules"]
tracked_extensions = [".Go", " txt", "GO", ""]
max_entries = 0
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
	})
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() error = %v", err)
	}

	// Relative paths resolve against the physical working directory, so
	// resolve symlinks (macOS: /var -> /private/var) in the expectation.
	resolvedTemp, err := filepath.EvalSymlinks(tempDir)
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}

	expectedWatch := []string{filepath.Join(home, "work"), filepath.Join(resolvedTemp, "rel")}
	slices.Sort(expectedWatch)
	if !slices.Equal(cfg.WatchPaths, expectedWatch) {
		t.Fatalf("WatchPaths = %v, want %v", cfg.WatchPaths, expectedWatch)
	}

	expectedIgnored := []string{"node_modules", "tmp"}
	slices.Sort(expectedIgnored)
	if !slices.Equal(cfg.IgnoredPaths, expectedIgnored) {
		t.Fatalf("IgnoredPaths = %v, want %v", cfg.IgnoredPaths, expectedIgnored)
	}

	expectedExts := []string{"go", "txt"}
	if !slices.Equal(cfg.TrackedExtensions, expectedExts) {
		t.Fatalf("TrackedExtensions = %v, want %v", cfg.TrackedExtensions, expectedExts)
	}

	if cfg.MaxEntries != DefaultMaxEntries {
		t.Fatalf("MaxEntries = %d, want %d", cfg.MaxEntries, DefaultMaxEntries)
	}
}
