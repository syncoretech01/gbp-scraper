package web

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

var (
	// ErrJobLabelsUnsupported indicates that the active repository cannot store
	// job tags, folders, or ownership labels.
	ErrJobLabelsUnsupported = errors.New("job labels are unavailable")
	// ErrInvalidJobLabel indicates a rejected tag, folder, or owner value.
	ErrInvalidJobLabel = errors.New("invalid job label")
)

const (
	// MaximumJobTagLength bounds one tag so a list of them stays readable in a
	// table cell.
	MaximumJobTagLength = 40
	// MaximumJobTags bounds how many tags one job may carry. A job needs a
	// handful of labels, not a taxonomy.
	MaximumJobTags = 12
	// MaximumJobFolderLength bounds a folder name.
	MaximumJobFolderLength = 60
	// MaximumJobOwnerLength bounds an ownership label. It is a local, free-text
	// name — the workspace has no user directory to validate against.
	MaximumJobOwnerLength = 60
)

// JobLabels is the operator-applied metadata that sits beside a job's
// lifecycle state: the folder it is filed under, the tags it carries, and the
// person or team it belongs to. None of it affects execution.
type JobLabels struct {
	JobID  string   `json:"job_id"`
	Tags   []string `json:"tags"`
	Folder string   `json:"folder"`
	Owner  string   `json:"owner"`
}

type jobLabelRepository interface {
	SetJobLabels(context.Context, string, JobLabels) error
	JobLabels(context.Context, string) (JobLabels, error)
	AllJobLabels(context.Context) (map[string]JobLabels, error)
}

// SupportsJobLabels reports whether job tags, folders, and owners can be
// stored. Every label control is hidden when it returns false, so the pages
// never show an input that has nowhere to save.
func (s *Service) SupportsJobLabels() bool {
	_, ok := s.repo.(jobLabelRepository)

	return ok
}

func (s *Service) jobLabelRepository() (jobLabelRepository, error) {
	repository, ok := s.repo.(jobLabelRepository)
	if !ok {
		return nil, ErrJobLabelsUnsupported
	}

	return repository, nil
}

// SetJobLabels validates and stores a job's folder, tags, and owner. It
// replaces the whole label set rather than merging, so removing a tag is
// possible with the same call that adds one.
func (s *Service) SetJobLabels(ctx context.Context, jobID string, labels JobLabels) error {
	repository, err := s.jobLabelRepository()
	if err != nil {
		return err
	}

	normalized, err := NormalizeJobLabels(labels)
	if err != nil {
		return err
	}

	normalized.JobID = jobID

	return repository.SetJobLabels(ctx, jobID, normalized)
}

// JobLabels reads one job's labels.
func (s *Service) JobLabels(ctx context.Context, jobID string) (JobLabels, error) {
	repository, err := s.jobLabelRepository()
	if err != nil {
		return JobLabels{}, err
	}

	return repository.JobLabels(ctx, jobID)
}

// AllJobLabels returns every job's labels keyed by job ID, so a list page can
// render tags and folders without one query per row.
func (s *Service) AllJobLabels(ctx context.Context) (map[string]JobLabels, error) {
	repository, err := s.jobLabelRepository()
	if err != nil {
		return nil, err
	}

	return repository.AllJobLabels(ctx)
}

// NormalizeJobLabels trims, bounds, de-duplicates, and orders a label set. It
// is exported so the API handler and the repository agree on exactly one
// definition of a valid label.
func NormalizeJobLabels(labels JobLabels) (JobLabels, error) {
	folder, err := normalizeJobLabelValue(labels.Folder, MaximumJobFolderLength, "folder")
	if err != nil {
		return JobLabels{}, err
	}

	owner, err := normalizeJobLabelValue(labels.Owner, MaximumJobOwnerLength, "owner")
	if err != nil {
		return JobLabels{}, err
	}

	seen := make(map[string]struct{}, len(labels.Tags))
	tags := make([]string, 0, len(labels.Tags))
	for _, raw := range labels.Tags {
		tag, tagErr := normalizeJobLabelValue(raw, MaximumJobTagLength, "tag")
		if tagErr != nil {
			return JobLabels{}, tagErr
		}
		if tag == "" {
			continue
		}
		key := strings.ToLower(tag)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		tags = append(tags, tag)
	}

	if len(tags) > MaximumJobTags {
		return JobLabels{}, fmt.Errorf("%w: at most %d tags are allowed", ErrInvalidJobLabel, MaximumJobTags)
	}

	sort.SliceStable(tags, func(left, right int) bool {
		return strings.ToLower(tags[left]) < strings.ToLower(tags[right])
	})

	return JobLabels{JobID: labels.JobID, Tags: tags, Folder: folder, Owner: owner}, nil
}

// ParseJobTagList splits the comma-separated form field the job pages submit.
func ParseJobTagList(raw string) []string {
	parts := strings.Split(raw, ",")
	tags := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			tags = append(tags, trimmed)
		}
	}

	return tags
}

func normalizeJobLabelValue(raw string, limit int, kind string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}

	// A control character would corrupt a table cell, a CSV export, and a log
	// line alike, so it is rejected rather than silently stripped.
	if strings.ContainsAny(value, "\x00\r\n\t") {
		return "", fmt.Errorf("%w: a %s cannot contain control characters", ErrInvalidJobLabel, kind)
	}

	if len([]rune(value)) > limit {
		return "", fmt.Errorf("%w: a %s is limited to %d characters", ErrInvalidJobLabel, kind, limit)
	}

	return value, nil
}
