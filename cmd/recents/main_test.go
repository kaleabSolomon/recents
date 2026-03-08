package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"recents/internal/storage"
)

func TestRenderListMarksMissingFiles(t *testing.T) {
	t.Parallel()

	m := model{
		records: []storage.FileRecord{
			{Name: "gone.txt", Directory: "/tmp", LastOpened: time.Now().Add(-time.Minute), Missing: true},
		},
		width:  80,
		height: 40,
	}

	view := m.View()
	if !strings.Contains(view, "❌ gone.txt") {
		t.Fatalf("expected ❌ marker in list view, got \n%s", view)
	}
}

func TestOpenExistingPathCmdFailsOnMissingPath(t *testing.T) {
	t.Parallel()

	msg := openExistingPathCmd("/tmp/does-not-exist-xyz", false)()
	res, ok := msg.(actionResultMsg)
	if !ok {
		t.Fatalf("unexpected message type %T", msg)
	}
	if res.err == nil {
		t.Fatalf("expected error for missing path")
	}
}

func TestOpenExistingPathCmdRejectsNonDirectoryWhenRequired(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	msg := openExistingPathCmd(file, true)()
	res, ok := msg.(actionResultMsg)
	if !ok {
		t.Fatalf("unexpected message type %T", msg)
	}
	if res.err == nil {
		t.Fatalf("expected directory validation error")
	}
}
