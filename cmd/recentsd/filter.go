package main

import (
	"os"
	"path/filepath"
	"strings"
)

type fileFilter struct {
	trackedExts map[string]struct{}
	ignored     []string
}

func newFileFilter(trackedExts []string, ignored []string) fileFilter {
	exts := make(map[string]struct{}, len(trackedExts))
	for _, ext := range trackedExts {
		ext = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(ext), "."))
		if ext == "" {
			continue
		}
		exts[ext] = struct{}{}
	}

	return fileFilter{
		trackedExts: exts,
		ignored:     ignored,
	}
}

func shouldTrackFile(path string, filter fileFilter) bool {
	clean := filepath.Clean(path)
	if clean == "." || clean == "" {
		return false
	}

	if isIgnoredPath(clean, filter.ignored) {
		return false
	}

	base := filepath.Base(clean)
	for _, suffix := range []string{".swp", ".tmp", ".part", "~"} {
		if strings.HasSuffix(strings.ToLower(base), suffix) {
			return false
		}
	}

	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(base)), ".")
	if _, ok := filter.trackedExts[ext]; !ok {
		return false
	}

	info, err := os.Stat(clean)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular()
}

func isIgnoredPath(path string, ignored []string) bool {
	cleanPath := filepath.Clean(path)
	sep := string(filepath.Separator)
	normalizedPath := sep + strings.Trim(cleanPath, sep) + sep
	pathParts := strings.Split(strings.Trim(cleanPath, sep), sep)

	for _, raw := range ignored {
		pattern := strings.TrimSpace(raw)
		if pattern == "" {
			continue
		}
		pattern = filepath.Clean(pattern)

		if filepath.IsAbs(pattern) {
			if cleanPath == pattern {
				return true
			}
			if strings.HasPrefix(cleanPath, pattern+sep) {
				return true
			}
			continue
		}

		if strings.Contains(pattern, sep) {
			needle := sep + strings.Trim(pattern, sep) + sep
			if strings.Contains(normalizedPath, needle) || strings.HasSuffix(cleanPath, pattern) {
				return true
			}
			continue
		}

		for _, part := range pathParts {
			if part == pattern {
				return true
			}
		}
	}

	return false
}
