package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsIgnoredPath(t *testing.T) {
	t.Parallel()

	t.Run("segment match", func(t *testing.T) {
		if !isIgnoredPath("/home/u/project/node_modules/pkg/file.js", []string{"node_modules"}) {
			t.Fatalf("expected segment ignore to match")
		}
	})

	t.Run("nested relative pattern", func(t *testing.T) {
		if !isIgnoredPath("/home/u/Downloads/tmp/file.txt", []string{"Downloads/tmp"}) {
			t.Fatalf("expected nested pattern ignore to match")
		}
	})

	t.Run("absolute prefix match", func(t *testing.T) {
		if !isIgnoredPath("/home/u/work/cache/a.txt", []string{"/home/u/work/cache"}) {
			t.Fatalf("expected absolute ignore to match")
		}
	})

	t.Run("non-match", func(t *testing.T) {
		if isIgnoredPath("/home/u/work/src/main.go", []string{"node_modules", ".git"}) {
			t.Fatalf("did not expect ignore to match")
		}
	})
}

func TestShouldTrackFile(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	validFile := filepath.Join(tempDir, "main.go")
	if err := os.WriteFile(validFile, []byte("package main"), 0o644); err != nil {
		t.Fatalf("write valid file: %v", err)
	}

	tmpFile := filepath.Join(tempDir, "scratch.tmp")
	if err := os.WriteFile(tmpFile, []byte("tmp"), 0o644); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}

	filter := newFileFilter([]string{"go", "txt"}, []string{"node_modules"})

	if !shouldTrackFile(validFile, filter) {
		t.Fatalf("expected valid tracked file to pass")
	}
	if shouldTrackFile(tmpFile, filter) {
		t.Fatalf("expected temp file to be filtered")
	}
	if shouldTrackFile(filepath.Join(tempDir, "missing.go"), filter) {
		t.Fatalf("expected missing file to be filtered")
	}
}
