package storage

import "recents/internal/identity"

func init() {
	getUserHomeDir = func() string {
		home, err := identity.HomeDir()
		if err != nil {
			return ""
		}
		return home
	}
}
