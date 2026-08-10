package web

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"
)

var (
	// ErrResultStoreUnsupported indicates that the configured repository only
	// supports the legacy CSV job API.
	ErrResultStoreUnsupported = errors.New("normalized result storage is unavailable")
	// ErrBusinessNotFound indicates that a normalized business ID is unknown.
	ErrBusinessNotFound = errors.New("business not found")
	// ErrInvalidResultQuery identifies a user-supplied filter or sort value that
	// is not part of the bounded local query language.
	ErrInvalidResultQuery = errors.New("invalid result query")
	// ErrInvalidResultMutation identifies an unsupported or unsafe workflow
	// update submitted for normalized businesses.
	ErrInvalidResultMutation = errors.New("invalid result mutation")
)

// ResultFileImport reports one idempotent legacy CSV import.
type ResultFileImport struct {
	JobID            string `json:"job_id"`
	Rows             int64  `json:"rows"`
	ImportedSources  int64  `json:"imported_sources"`
	UniqueBusinesses int64  `json:"unique_businesses"`
	Duplicates       int64  `json:"duplicates"`
	Warnings         int64  `json:"warnings"`
	SkippedUnchanged bool   `json:"skipped_unchanged"`
	Checksum         string `json:"checksum"`
}

// ResultFilter is one validated database filter. Supported fields and
// operators are deliberately enumerated by the repository; values are always
// bound parameters.
type ResultFilter struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}

// ResultSearch controls indexed result queries and pagination.
type ResultSearch struct {
	Query             string         `json:"query,omitempty"`
	JobID             string         `json:"job_id,omitempty"`
	Sort              string         `json:"sort,omitempty"`
	Filters           []ResultFilter `json:"filters,omitempty"`
	IncludeDuplicates bool           `json:"include_duplicates"`
	Limit             int            `json:"limit"`
	Offset            int            `json:"offset"`
}

// BusinessResult is the normalized, display-safe row returned by result
// searches. Raw source values and provenance are available through the detail
// endpoint rather than being duplicated into every table response.
type BusinessResult struct {
	ID                   string    `json:"id"`
	SourceRecordID       int64     `json:"source_record_id,omitempty"`
	Name                 string    `json:"name"`
	PrimaryCategory      string    `json:"primary_category"`
	AdditionalCategories string    `json:"additional_categories,omitempty"`
	BusinessStatus       string    `json:"business_status,omitempty"`
	Claimed              bool      `json:"claimed"`
	Address              string    `json:"address"`
	City                 string    `json:"city,omitempty"`
	State                string    `json:"state,omitempty"`
	PostalCode           string    `json:"postal_code,omitempty"`
	Country              string    `json:"country,omitempty"`
	Latitude             *float64  `json:"latitude,omitempty"`
	Longitude            *float64  `json:"longitude,omitempty"`
	Phone                string    `json:"phone,omitempty"`
	NormalizedPhone      string    `json:"normalized_phone,omitempty"`
	PrimaryEmail         string    `json:"primary_email,omitempty"`
	Website              string    `json:"website,omitempty"`
	Domain               string    `json:"domain,omitempty"`
	WebsiteStatus        string    `json:"website_status"`
	WebsiteResponseMS    *int64    `json:"website_response_ms,omitempty"`
	Rating               *float64  `json:"rating,omitempty"`
	ReviewCount          *int64    `json:"review_count,omitempty"`
	QualityScore         float64   `json:"quality_score"`
	Confidence           float64   `json:"confidence"`
	Reviewed             bool      `json:"reviewed"`
	Notes                string    `json:"notes,omitempty"`
	ChangeStatus         string    `json:"change_status"`
	Tags                 []string  `json:"tags,omitempty"`
	MapsURL              string    `json:"maps_url,omitempty"`
	PlaceID              string    `json:"place_id,omitempty"`
	CID                  string    `json:"cid,omitempty"`
	DataID               string    `json:"data_id,omitempty"`
	SourceJobID          string    `json:"source_job_id,omitempty"`
	SourceQuery          string    `json:"source_query,omitempty"`
	SourceCell           string    `json:"source_cell,omitempty"`
	ScrapedAt            time.Time `json:"scraped_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// ResultPage contains one deterministic page plus the total match count.
type ResultPage struct {
	Results []BusinessResult `json:"results"`
	Total   int64            `json:"total"`
	Limit   int              `json:"limit"`
	Offset  int              `json:"offset"`
}

// ResultOverview contains database-wide normalized data quality counts.
type ResultOverview struct {
	UniqueBusinesses int64 `json:"unique_businesses"`
	RawRecords       int64 `json:"raw_records"`
	DuplicateGroups  int64 `json:"duplicate_groups"`
	DuplicatesMerged int64 `json:"duplicates_merged"`
	Websites         int64 `json:"websites"`
	Emails           int64 `json:"emails"`
	Phones           int64 `json:"phones"`
	NeedsReview      int64 `json:"needs_review"`
}

// BusinessSourceView exposes source-level provenance without credentials.
type BusinessSourceView struct {
	ID               int64     `json:"id"`
	JobID            string    `json:"job_id,omitempty"`
	SourceType       string    `json:"source_type"`
	SourceURL        string    `json:"source_url,omitempty"`
	SourceQuery      string    `json:"source_query,omitempty"`
	SourceCell       string    `json:"source_cell,omitempty"`
	InputID          string    `json:"input_id,omitempty"`
	ExtractionMethod string    `json:"extraction_method"`
	Confidence       float64   `json:"confidence"`
	ExtractedAt      time.Time `json:"extracted_at"`
}

// BusinessVersionView is an immutable normalized snapshot reference.
type BusinessVersionView struct {
	ID            int64     `json:"id"`
	Version       int64     `json:"version"`
	ChangeType    string    `json:"change_type"`
	ChangedFields []string  `json:"changed_fields"`
	ObservedAt    time.Time `json:"observed_at"`
}

// BusinessDetail combines the preferred row with its local history and raw
// normalized JSON snapshot.
type BusinessDetail struct {
	Business   BusinessResult        `json:"business"`
	RawJSON    string                `json:"raw_json"`
	Sources    []BusinessSourceView  `json:"sources"`
	Versions   []BusinessVersionView `json:"versions"`
	Duplicates []string              `json:"duplicate_ids,omitempty"`
}

// ResultRepository is an additive local-data capability. Keeping it separate
// preserves compatibility for embedders that implement only JobRepository.
type ResultRepository interface {
	ImportLegacyCSV(context.Context, Job, string) (ResultFileImport, error)
	SearchBusinesses(context.Context, ResultSearch) (ResultPage, error)
	GetBusiness(context.Context, string) (BusinessDetail, error)
	ResultOverview(context.Context) (ResultOverview, error)
}

// ResultMutation describes one bounded, auditable workflow change. Supported
// actions are tag, untag, reviewed, unreviewed, and notes.
type ResultMutation struct {
	IDs    []string `json:"ids"`
	Action string   `json:"action"`
	Value  string   `json:"value,omitempty"`
}

type resultMutationRepository interface {
	MutateBusinesses(context.Context, ResultMutation) (int64, error)
}

func (s *Server) resultMutationAvailable() bool {
	if s == nil || s.svc == nil {
		return false
	}
	_, ok := s.svc.repo.(resultMutationRepository)
	return ok
}

// MutateBusinesses applies a local workflow update without modifying source
// observations or historical versions.
func (s *Service) MutateBusinesses(ctx context.Context, mutation ResultMutation) (int64, error) {
	repository, ok := s.repo.(resultMutationRepository)
	if !ok {
		return 0, ErrResultStoreUnsupported
	}
	return repository.MutateBusinesses(ctx, mutation)
}

// ImportJobResults idempotently imports a job's untouched legacy CSV into the
// normalized local database.
func (s *Service) ImportJobResults(ctx context.Context, id string) (ResultFileImport, error) {
	repository, ok := s.repo.(ResultRepository)
	if !ok {
		return ResultFileImport{}, ErrResultStoreUnsupported
	}

	job, err := s.Get(ctx, id)
	if err != nil {
		return ResultFileImport{}, err
	}

	path, err := s.GetCSV(ctx, id)
	if err != nil {
		return ResultFileImport{}, err
	}

	return repository.ImportLegacyCSV(ctx, job, path)
}

// BackfillLegacyResults imports every existing UUID CSV from oldest to newest
// so the preferred business record reflects the latest observation. Missing
// CSVs are expected for drafts and queued jobs and are skipped.
func (s *Service) BackfillLegacyResults(ctx context.Context) ([]ResultFileImport, error) {
	if _, ok := s.repo.(ResultRepository); !ok {
		return nil, ErrResultStoreUnsupported
	}

	jobs, err := s.All(ctx)
	if err != nil {
		return nil, err
	}

	sort.SliceStable(jobs, func(left, right int) bool {
		if jobs[left].Date.Equal(jobs[right].Date) {
			return jobs[left].ID < jobs[right].ID
		}

		return jobs[left].Date.Before(jobs[right].Date)
	})

	imports := make([]ResultFileImport, 0, len(jobs))
	failures := make([]error, 0)
	for _, job := range jobs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		path, pathErr := s.csvPath(job.ID)
		if pathErr != nil {
			failures = append(failures, fmt.Errorf("inspect results for job %s: %w", job.ID, pathErr))

			continue
		}
		if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
			continue
		} else if statErr != nil {
			failures = append(failures, fmt.Errorf("inspect results for job %s: %w", job.ID, statErr))

			continue
		}

		result, importErr := s.ImportJobResults(ctx, job.ID)
		if importErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			failures = append(failures, fmt.Errorf("import results for job %s: %w", job.ID, importErr))

			continue
		}
		imports = append(imports, result)
	}

	return imports, errors.Join(failures...)
}

// SearchBusinesses executes an indexed normalized result query.
func (s *Service) SearchBusinesses(ctx context.Context, search ResultSearch) (ResultPage, error) {
	repository, ok := s.repo.(ResultRepository)
	if !ok {
		return ResultPage{}, ErrResultStoreUnsupported
	}

	return repository.SearchBusinesses(ctx, search)
}

// GetBusiness returns one normalized record and its local provenance.
func (s *Service) GetBusiness(ctx context.Context, id string) (BusinessDetail, error) {
	repository, ok := s.repo.(ResultRepository)
	if !ok {
		return BusinessDetail{}, ErrResultStoreUnsupported
	}

	return repository.GetBusiness(ctx, id)
}

// ResultOverview returns normalized database quality counts.
func (s *Service) ResultOverview(ctx context.Context) (ResultOverview, error) {
	repository, ok := s.repo.(ResultRepository)
	if !ok {
		return ResultOverview{}, ErrResultStoreUnsupported
	}

	return repository.ResultOverview(ctx)
}
