package web

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// ResultStats is a cheap, file-backed summary used while legacy CSV files are
// being imported into the searchable local database. It intentionally treats
// the CSV as read-only so upgrades cannot damage an existing export.
type ResultStats struct {
	Rows             int       `json:"rows"`
	UniqueBusinesses int       `json:"unique_businesses"`
	Duplicates       int       `json:"duplicates"`
	WithWebsite      int       `json:"with_website"`
	WithPhone        int       `json:"with_phone"`
	WithEmail        int       `json:"with_email"`
	FileSizeBytes    int64     `json:"file_size_bytes"`
	FileUpdatedAt    time.Time `json:"file_updated_at"`
}

// GetResultStats summarizes one job's legacy CSV without changing it.
func (s *Service) GetResultStats(ctx context.Context, id string) (ResultStats, error) {
	path, err := s.csvPath(id)
	if err != nil {
		return ResultStats{}, err
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ResultStats{}, fmt.Errorf("csv file not found for job %s: %w", id, ErrPlacesNotFound)
		}

		return ResultStats{}, err
	}

	file, err := os.Open(path)
	if err != nil {
		return ResultStats{}, err
	}
	defer func() { _ = file.Close() }()

	stats, err := summarizeResults(ctx, file)
	if err != nil {
		return ResultStats{}, err
	}

	stats.FileSizeBytes = info.Size()
	stats.FileUpdatedAt = info.ModTime().UTC()

	return stats, nil
}

func summarizeResults(ctx context.Context, source io.Reader) (ResultStats, error) {
	reader := csv.NewReader(source)
	reader.FieldsPerRecord = -1

	header, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return ResultStats{}, nil
	}

	if err != nil {
		return ResultStats{}, err
	}

	columns := make(map[string]int, len(header))
	for index, name := range header {
		columns[strings.ToLower(strings.TrimSpace(name))] = index
	}

	value := func(row []string, name string) string {
		index, ok := columns[name]
		if !ok || index < 0 || index >= len(row) {
			return ""
		}

		return strings.TrimSpace(row[index])
	}

	present := func(raw string) bool {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "", "null", "[]", "{}":
			return false
		default:
			return true
		}
	}

	seen := make(map[string]struct{})
	stats := ResultStats{}

	for {
		if err := ctx.Err(); err != nil {
			return ResultStats{}, err
		}

		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return ResultStats{}, err
		}

		stats.Rows++

		if present(value(row, "website")) {
			stats.WithWebsite++
		}

		if present(value(row, "phone")) {
			stats.WithPhone++
		}

		if present(value(row, "emails")) {
			stats.WithEmail++
		}

		key := stableBusinessKey(
			value(row, "place_id"),
			value(row, "cid"),
			value(row, "data_id"),
			value(row, "title"),
			value(row, "address"),
		)

		if key == "" {
			key = fmt.Sprintf("row:%d", stats.Rows)
		}

		if _, exists := seen[key]; exists {
			stats.Duplicates++
			continue
		}

		seen[key] = struct{}{}
	}

	stats.UniqueBusinesses = len(seen)

	return stats, nil
}

func stableBusinessKey(placeID, cid, dataID, title, address string) string {
	if placeID != "" {
		return "place:" + placeID
	}

	if cid != "" {
		return "cid:" + cid
	}

	if dataID != "" {
		return "data:" + dataID
	}

	if title != "" || address != "" {
		return "name-address:" + strings.ToLower(title+"\x00"+address)
	}

	return ""
}
