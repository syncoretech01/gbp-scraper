package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrJobOrganisationUnsupported indicates the repository cannot store job
	// metadata such as archive state, name changes, or notes.
	ErrJobOrganisationUnsupported = errors.New("job organisation is unavailable")
	// ErrInvalidJobOrganisation indicates a rejected rename, archive, or note.
	ErrInvalidJobOrganisation = errors.New("invalid job organisation change")
)

// MaximumJobNameLength bounds a renamed job so lists stay readable.
const MaximumJobNameLength = 120

// MaximumJobNotesLength bounds an operator note on a job.
const MaximumJobNotesLength = 5000

// JobOrganisation is the metadata that sits beside a job's lifecycle state.
type JobOrganisation struct {
	JobID    string `json:"job_id"`
	Archived bool   `json:"archived"`
	Folder   string `json:"folder,omitempty"`
	Notes    string `json:"notes,omitempty"`
}

type jobOrganisationRepository interface {
	RenameJob(context.Context, string, string) error
	SetJobArchived(context.Context, string, bool) error
	SetJobNotes(context.Context, string, string) error
	JobOrganisation(context.Context, string) (JobOrganisation, error)
	ArchivedJobIDs(context.Context) (map[string]struct{}, error)
}

// SupportsJobOrganisation reports whether job metadata can be stored.
func (s *Service) SupportsJobOrganisation() bool {
	_, ok := s.repo.(jobOrganisationRepository)

	return ok
}

func (s *Service) jobOrganisationRepository() (jobOrganisationRepository, error) {
	repository, ok := s.repo.(jobOrganisationRepository)
	if !ok {
		return nil, ErrJobOrganisationUnsupported
	}

	return repository, nil
}

// RenameJob changes a job's display name without touching its run state.
func (s *Service) RenameJob(ctx context.Context, jobID, name string) error {
	repository, err := s.jobOrganisationRepository()
	if err != nil {
		return err
	}

	name = strings.TrimSpace(name)
	if name == "" || len(name) > MaximumJobNameLength {
		return fmt.Errorf("%w: name must be 1 to %d characters", ErrInvalidJobOrganisation, MaximumJobNameLength)
	}

	if strings.ContainsAny(name, "\x00\r\n") {
		return fmt.Errorf("%w: name contains control characters", ErrInvalidJobOrganisation)
	}

	return repository.RenameJob(ctx, jobID, name)
}

// SetJobArchived hides a finished job from the default queue view, or restores it.
func (s *Service) SetJobArchived(ctx context.Context, jobID string, archived bool) error {
	repository, err := s.jobOrganisationRepository()
	if err != nil {
		return err
	}

	return repository.SetJobArchived(ctx, jobID, archived)
}

// SetJobNotes stores an operator note against a job.
func (s *Service) SetJobNotes(ctx context.Context, jobID, notes string) error {
	repository, err := s.jobOrganisationRepository()
	if err != nil {
		return err
	}

	if len(notes) > MaximumJobNotesLength {
		return fmt.Errorf("%w: notes must be at most %d characters", ErrInvalidJobOrganisation, MaximumJobNotesLength)
	}

	return repository.SetJobNotes(ctx, jobID, strings.TrimSpace(notes))
}

// GetJobOrganisation reads a job's metadata.
func (s *Service) GetJobOrganisation(ctx context.Context, jobID string) (JobOrganisation, error) {
	repository, err := s.jobOrganisationRepository()
	if err != nil {
		return JobOrganisation{}, err
	}

	return repository.JobOrganisation(ctx, jobID)
}

// ArchivedJobIDs returns the set of jobs hidden from the default queue view.
func (s *Service) ArchivedJobIDs(ctx context.Context) (map[string]struct{}, error) {
	repository, err := s.jobOrganisationRepository()
	if err != nil {
		return nil, err
	}

	return repository.ArchivedJobIDs(ctx)
}
