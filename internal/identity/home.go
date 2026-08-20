package identity

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// HomeDir resolves the effective user home for recents.
// Priority:
// 1. RECENTS_HOME override
// 2. RECENTS_USER user's home (set by the systemd unit)
// 3. SUDO_UID invoking user home when running under sudo
// 4. process user's home directory
func HomeDir() (string, error) {
	if v := strings.TrimSpace(os.Getenv("RECENTS_HOME")); v != "" {
		return filepath.Clean(v), nil
	}

	if name := strings.TrimSpace(os.Getenv("RECENTS_USER")); name != "" {
		u, err := user.Lookup(name)
		if err == nil && strings.TrimSpace(u.HomeDir) != "" {
			return filepath.Clean(u.HomeDir), nil
		}
	}

	if sudoUID := strings.TrimSpace(os.Getenv("SUDO_UID")); sudoUID != "" {
		if _, err := strconv.Atoi(sudoUID); err == nil {
			u, err := user.LookupId(sudoUID)
			if err == nil && strings.TrimSpace(u.HomeDir) != "" {
				return filepath.Clean(u.HomeDir), nil
			}
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Clean(home), nil
}

// UID resolves the uid whose file opens should be recorded, mirroring the
// HomeDir resolution order for users:
// 1. RECENTS_USER (set by the systemd unit, which runs the daemon as root)
// 2. SUDO_UID when running under sudo
// 3. the process uid
func UID() int {
	if name := strings.TrimSpace(os.Getenv("RECENTS_USER")); name != "" {
		if u, err := user.Lookup(name); err == nil {
			if uid, err := strconv.Atoi(u.Uid); err == nil {
				return uid
			}
		}
	}

	if sudoUID := strings.TrimSpace(os.Getenv("SUDO_UID")); sudoUID != "" {
		if uid, err := strconv.Atoi(sudoUID); err == nil {
			return uid
		}
	}

	return os.Getuid()
}
