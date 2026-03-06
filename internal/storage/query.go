package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type FileRecord struct {
	ID         int64
	Path       string
	Name       string
	Extension  string
	LastOpened time.Time
	FirstSeen  time.Time
	OpenCount  int
	Directory  string
	Missing    bool
}

type QueryOptions struct {
	Search     string
	Extensions []string
	Limit      int
}

func (s *Store) ListRecent(ctx context.Context, opts QueryOptions) ([]FileRecord, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 200
	}

	search := strings.TrimSpace(strings.ToLower(opts.Search))
	extensions := normalizeQueryExtensions(opts.Extensions)

	query := `
SELECT id, path, name, extension, last_opened, first_seen, open_count, directory
FROM files`
	var where []string
	var args []any

	if search != "" {
		where = append(where, "LOWER(name) LIKE ?")
		args = append(args, "%"+search+"%")
	}

	if len(extensions) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(extensions)), ",")
		where = append(where, fmt.Sprintf("extension IN (%s)", placeholders))
		for _, ext := range extensions {
			args = append(args, ext)
		}
	}

	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY last_opened DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query recent files: %w", err)
	}
	defer rows.Close()

	results := make([]FileRecord, 0, limit)
	for rows.Next() {
		var rec FileRecord
		if err := rows.Scan(
			&rec.ID,
			&rec.Path,
			&rec.Name,
			&rec.Extension,
			&rec.LastOpened,
			&rec.FirstSeen,
			&rec.OpenCount,
			&rec.Directory,
		); err != nil {
			return nil, fmt.Errorf("scan recent file row: %w", err)
		}
		results = append(results, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent file rows: %w", err)
	}

	for i := range results {
		if _, err := os.Stat(results[i].Path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				results[i].Missing = true
				continue
			}
		}
	}

	return results, nil
}

func normalizeQueryExtensions(input []string) []string {
	seen := make(map[string]struct{}, len(input))
	result := make([]string, 0, len(input))
	for _, ext := range input {
		ext = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(ext), "."))
		if ext == "" {
			continue
		}
		if _, ok := seen[ext]; ok {
			continue
		}
		seen[ext] = struct{}{}
		result = append(result, ext)
	}
	return result
}

func FormatHomePath(path string) string {
	homeRaw := homeDir()
	if homeRaw == "" {
		return filepath.Clean(path)
	}

	home, err := filepath.Abs(filepath.Clean(homeRaw))
	if err != nil {
		return path
	}
	cleanPath := filepath.Clean(path)
	if cleanPath == home {
		return "~"
	}
	prefix := home + string(filepath.Separator)
	if strings.HasPrefix(cleanPath, prefix) {
		return "~" + string(filepath.Separator) + strings.TrimPrefix(cleanPath, prefix)
	}
	return cleanPath
}

var homeDir = func() string {
	return strings.TrimSpace(getUserHomeDir())
}

var getUserHomeDir = func() string {
	return ""
}
