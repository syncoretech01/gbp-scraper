package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrDuplicateReviewUnsupported indicates the repository cannot resolve
	// duplicate candidates.
	ErrDuplicateReviewUnsupported = errors.New("duplicate review is unavailable")
	// ErrDuplicateCandidateNotFound indicates the candidate pair does not exist.
	ErrDuplicateCandidateNotFound = errors.New("duplicate candidate not found")
	// ErrDuplicateAlreadyResolved indicates an operator already decided the pair.
	ErrDuplicateAlreadyResolved = errors.New("duplicate candidate is already resolved")
	// ErrInvalidDuplicateDecision indicates a malformed review decision.
	ErrInvalidDuplicateDecision = errors.New("invalid duplicate decision")
)

// DuplicateDecision is one operator judgement on a candidate pair.
type DuplicateDecision struct {
	CandidateID int64  `json:"candidate_id"`
	Action      string `json:"action"`
	// KeepBusinessID names the record that survives a merge. When empty the
	// first record of the pair is kept.
	KeepBusinessID string `json:"keep_business_id,omitempty"`
	Note           string `json:"note,omitempty"`
	Operator       string `json:"operator,omitempty"`
}

// DuplicateResolution is the durable outcome of a decision.
type DuplicateResolution struct {
	CandidateID      int64  `json:"candidate_id"`
	Action           string `json:"action"`
	State            string `json:"state"`
	KeptBusinessID   string `json:"kept_business_id"`
	MergedBusinessID string `json:"merged_business_id,omitempty"`
}

// DuplicateReviewSide is one half of a side-by-side comparison.
type DuplicateReviewSide struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address,omitempty"`
	Domain  string `json:"domain,omitempty"`
}

// DuplicateReviewPair is a pending candidate presented for review.
type DuplicateReviewPair struct {
	CandidateID int64               `json:"candidate_id"`
	Score       float64             `json:"score"`
	Signals     string              `json:"signals"`
	Left        DuplicateReviewSide `json:"left"`
	Right       DuplicateReviewSide `json:"right"`
	CreatedAt   time.Time           `json:"created_at"`
}

type duplicateRepository interface {
	ResolveDuplicateCandidate(context.Context, DuplicateDecision) (DuplicateResolution, error)
	ListDuplicateCandidates(context.Context, int) ([]DuplicateReviewPair, error)
}

// SupportsDuplicateReview reports whether duplicate decisions can be recorded.
func (s *Service) SupportsDuplicateReview() bool {
	_, ok := s.repo.(duplicateRepository)

	return ok
}

// ListDuplicateCandidates returns pending pairs for side-by-side review.
func (s *Service) ListDuplicateCandidates(ctx context.Context, limit int) ([]DuplicateReviewPair, error) {
	repository, ok := s.repo.(duplicateRepository)
	if !ok {
		return nil, ErrDuplicateReviewUnsupported
	}

	return repository.ListDuplicateCandidates(ctx, limit)
}

// ResolveDuplicateCandidate applies one operator decision.
//
// Merging is non-destructive: the merged record keeps its row, its versions and
// its provenance, and its source observations move to the surviving business so
// no query, cell or timestamp is lost.
func (s *Service) ResolveDuplicateCandidate(
	ctx context.Context,
	decision DuplicateDecision,
) (DuplicateResolution, error) {
	repository, ok := s.repo.(duplicateRepository)
	if !ok {
		return DuplicateResolution{}, ErrDuplicateReviewUnsupported
	}

	if decision.CandidateID <= 0 {
		return DuplicateResolution{}, fmt.Errorf("%w: a candidate is required", ErrInvalidDuplicateDecision)
	}

	decision.Action = strings.ToLower(strings.TrimSpace(decision.Action))
	switch decision.Action {
	case "merge", "keep_both", "ignore":
	default:
		return DuplicateResolution{}, fmt.Errorf(
			"%w: action must be merge, keep_both or ignore", ErrInvalidDuplicateDecision,
		)
	}

	if len(decision.Note) > 500 {
		return DuplicateResolution{}, fmt.Errorf("%w: note is too long", ErrInvalidDuplicateDecision)
	}

	if keep := strings.TrimSpace(decision.KeepBusinessID); keep != "" && !validBusinessID(keep) {
		return DuplicateResolution{}, fmt.Errorf("%w: invalid business ID", ErrInvalidDuplicateDecision)
	}

	return repository.ResolveDuplicateCandidate(ctx, decision)
}
