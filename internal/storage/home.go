package storage

import "os"

func init() {
	getUserHomeDir = func() string {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return home
	}
}
