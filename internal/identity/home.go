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
// 2. SUDO_UID invoking user home when running under sudo
// 3. process user's home directory
func HomeDir() (string, error) {
	if v := strings.TrimSpace(os.Getenv("RECENTS_HOME")); v != "" {
		return filepath.Clean(v), nil
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
