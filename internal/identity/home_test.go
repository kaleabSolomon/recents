package identity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHomeDirRecentsHomeOverride(t *testing.T) {
	t.Setenv("RECENTS_HOME", "/home/someone//")
	t.Setenv("RECENTS_USER", "")
	t.Setenv("SUDO_UID", "")

	home, err := HomeDir()
	if err != nil {
		t.Fatalf("HomeDir() error = %v", err)
	}
	if want := filepath.Clean("/home/someone"); home != want {
		t.Fatalf("HomeDir() = %q, want %q", home, want)
	}
}

func TestUIDSudoFallback(t *testing.T) {
	t.Setenv("RECENTS_USER", "")
	t.Setenv("SUDO_UID", "1234")

	if got := UID(); got != 1234 {
		t.Fatalf("UID() = %d, want 1234", got)
	}
}

func TestUIDDefaultsToProcessUID(t *testing.T) {
	t.Setenv("RECENTS_USER", "")
	t.Setenv("SUDO_UID", "")

	if got := UID(); got != os.Getuid() {
		t.Fatalf("UID() = %d, want %d", got, os.Getuid())
	}
}
