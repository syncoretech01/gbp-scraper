package web

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"github.com/gosom/google-maps-scraper/web/prospect"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ResultStats is a cheap, file-backed summary used while legacy CSV files are
// being imported into the searchable local database. It intentionally treats
// the CSV as read-only so upgrades cannot damage an existing export.
type ResultStats struct {
	Rows int `json:"rows"`
	// UniqueBusinesses is how many distinct businesses the file holds, and
	// Duplicates is how many of its rows repeat a business an earlier row in
	// the same file already carried.
	//
	// Duplicates is a property of this one file and nothing else. It is not
	// an entity merge, it is not the number of times a business was seen
	// during the run, and on a Maps job it is normally zero because the
	// merge that writes the file has already collapsed repeats. Anything
	// presenting it to an operator must say what it counts; see
	// RunObservations for the run-level vocabulary.
	UniqueBusinesses int       `json:"unique_businesses"`
	Duplicates       int       `json:"duplicates"`
	WithWebsite      int       `json:"with_website"`
	WithPhone        int       `json:"with_phone"`
	WithEmail        int       `json:"with_email"`
	FileSizeBytes    int64     `json:"file_size_bytes"`
	FileUpdatedAt    time.Time `json:"file_updated_at"`
	// Geography is where the collected businesses sit relative to the area
	// the job was configured to search. It is additive evidence: a consumer
	// that does not know about it ignores the field, and a job without
	// usable centre coordinates reports Available false rather than zeros.
	Geography ResultGeography `json:"geography"`
	// Run is this job counted in the vocabulary of web/run_metrics.go:
	// observations, repeated observations, unique businesses. The file alone
	// cannot supply it — a committed CSV has already had its repeats folded
	// away — so it comes from the durable per-task checkpoints and reports
	// Available false wherever those are not kept.
	Run RunObservations `json:"run"`
}

// GetResultStats summarizes one job's legacy CSV without changing it.
//
// The job's own configuration is read alongside the file so the summary can
// report where the collected businesses sit relative to the configured search
// area. That lookup is best effort: a job row that cannot be read leaves the
// geography unavailable and every other counter unchanged.
func (s *Service) GetResultStats(ctx context.Context, id string) (ResultStats, error) {
	// The identifier is validated into a path before anything else touches it,
	// so a traversal attempt is refused without a database round trip.
	path, err := s.csvPath(id)
	if err != nil {
		return ResultStats{}, err
	}

	area := s.jobSearchArea(ctx, id)

	stats, err := s.resultStatsWithin(ctx, path, area)
	if err != nil {
		return ResultStats{}, err
	}

	stats.Run = s.jobRunObservations(ctx, id).WithUniqueBusinesses(int64(stats.UniqueBusinesses))
	if pairs, known := s.JobOpenDuplicateCandidates(ctx, id); known {
		stats.Run = stats.Run.WithUnresolvedDuplicates(pairs)
	}
	if stats.Geography.Available {
		if spillover, spillErr := s.JobCellSpillover(ctx, id, area.CellKM); spillErr == nil {
			stats.Geography.Spillover = spillover
		}
	}

	return stats, nil
}

// jobRunObservations reads how often each finished search re-found a business
// the run already had. That evidence lives only in the durable task
// checkpoints — the committed file has already folded the repeats away — and
// it is what separates "331 businesses" from "555 observations of them".
//
// It reads the task rows directly rather than going through JobCoverage: this
// summary is built once per job on pages that list every job, and the full
// coverage report also assembles a per-query table, a completion trend and a
// per-cell confidence model that nothing here reads.
//
// Like the geography lookup it is best effort: an installation without the
// coverage engine reports an unavailable value, and every other counter in the
// summary is unaffected.
func (s *Service) jobRunObservations(ctx context.Context, id string) RunObservations {
	repository, err := s.coverageRepository()
	if err != nil {
		return RunObservations{}
	}

	rows, err := repository.JobCoverageTasks(ctx, id)
	if err != nil {
		return RunObservations{}
	}

	totals := CoverageTotals{TasksTotal: int64(len(rows))}
	for _, row := range rows {
		totals.RowsAdded += max(row.RowsAdded, 0)
		totals.RowsReplaced += max(row.RowsReplaced, 0)
		totals.DuplicatesSkipped += max(row.DuplicatesSkipped, 0)
		if row.State == "completed" {
			totals.TasksDone++
		}
	}

	return NewRunObservations(totals)
}

// jobSearchArea resolves the geography a job was configured to search. It is
// best effort by design: a repository that is absent or cannot serve the job
// leaves the summary without a geographic section rather than failing a page
// that is otherwise perfectly serviceable.
func (s *Service) jobSearchArea(ctx context.Context, id string) JobSearchArea {
	if s.repo == nil {
		return JobSearchArea{}
	}

	job, err := s.repo.Get(ctx, id)
	if err != nil {
		return JobSearchArea{}
	}

	return NewJobSearchArea(job.Data)
}

func (s *Service) resultStatsWithin(ctx context.Context, path string, area JobSearchArea) (ResultStats, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ResultStats{}, fmt.Errorf("csv file not found for job %s: %w",
				strings.TrimSuffix(filepath.Base(path), ".csv"), ErrPlacesNotFound)
		}

		return ResultStats{}, err
	}

	file, err := os.Open(path)
	if err != nil {
		return ResultStats{}, err
	}
	defer func() { _ = file.Close() }()

	stats, err := summarizeResultsWithin(ctx, file, area)
	if err != nil {
		return ResultStats{}, err
	}

	stats.FileSizeBytes = info.Size()
	stats.FileUpdatedAt = info.ModTime().UTC()

	return stats, nil
}

func summarizeResults(ctx context.Context, source io.Reader) (ResultStats, error) {
	return summarizeResultsWithin(ctx, source, JobSearchArea{})
}

func summarizeResultsWithin(ctx context.Context, source io.Reader, area JobSearchArea) (ResultStats, error) {
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
	geography := newGeographyAccumulator(area)

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

		if website := value(row, "website"); present(website) && !prospect.IsSocialWebsite(website) {
			stats.WithWebsite++
		}

		if present(value(row, "phone")) {
			stats.WithPhone++
		}

		if present(value(row, "emails")) {
			stats.WithEmail++
		}

		latitude, longitude, hasCoordinates := parseCoordinatePair(
			value(row, "latitude"),
			value(row, "longitude"),
		)
		geography.observe(value(row, "title"), latitude, longitude, hasCoordinates)

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
	stats.Geography = geography.result()

	return stats, nil
}

// parseCoordinatePair reads one CSV row's position. A row missing either half
// of the pair has no position at all, which the caller records as such rather
// than treating as the origin.
func parseCoordinatePair(rawLatitude, rawLongitude string) (float64, float64, bool) {
	if rawLatitude == "" || rawLongitude == "" {
		return 0, 0, false
	}

	latitude, latErr := strconv.ParseFloat(rawLatitude, 64)
	longitude, lonErr := strconv.ParseFloat(rawLongitude, 64)
	if latErr != nil || lonErr != nil {
		return 0, 0, false
	}

	if !usableCoordinate(latitude, longitude) {
		return 0, 0, false
	}

	return latitude, longitude, true
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
