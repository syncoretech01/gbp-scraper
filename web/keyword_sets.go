package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

var (
	// ErrKeywordSetsUnsupported indicates the repository cannot store
	// reusable keyword sets.
	ErrKeywordSetsUnsupported = errors.New("keyword sets are unavailable")
	// ErrKeywordSetNotFound indicates the requested keyword set does not exist.
	ErrKeywordSetNotFound = errors.New("keyword set not found")
	// ErrInvalidKeywordSet indicates a rejected keyword set save.
	ErrInvalidKeywordSet = errors.New("invalid keyword set")
)

const (
	// MaximumKeywordSetNameLength bounds a set name so pickers stay readable.
	MaximumKeywordSetNameLength = 64
	// MaximumKeywordSetDescriptionLength bounds the optional description.
	MaximumKeywordSetDescriptionLength = 200
	// MaximumKeywordSetKeywords bounds how many query lines one set may hold.
	MaximumKeywordSetKeywords = 1000
	// MaximumKeywordSetKeywordLength bounds one query line.
	MaximumKeywordSetKeywordLength = 200
	// maximumKeywordSetList bounds a listing so the wizard picker stays small.
	maximumKeywordSetList = 200
)

// KeywordSet is a reusable, named list of Maps queries for the wizard.
type KeywordSet struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Keywords    []string   `json:"keywords"`
	UseCount    int        `json:"use_count"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type keywordSetRepository interface {
	ListKeywordSets(context.Context, int) ([]KeywordSet, error)
	SaveKeywordSet(context.Context, KeywordSet) (KeywordSet, error)
	DeleteKeywordSet(context.Context, string) error
	TouchKeywordSetUse(context.Context, string, time.Time) (KeywordSet, error)
}

// SupportsKeywordSets reports whether reusable keyword sets can be stored.
func (s *Service) SupportsKeywordSets() bool {
	_, ok := s.repo.(keywordSetRepository)

	return ok
}

func (s *Service) keywordSetRepository() (keywordSetRepository, error) {
	repository, ok := s.repo.(keywordSetRepository)
	if !ok {
		return nil, ErrKeywordSetsUnsupported
	}

	return repository, nil
}

// ListKeywordSets returns saved sets, newest first, bounded for the picker.
func (s *Service) ListKeywordSets(ctx context.Context) ([]KeywordSet, error) {
	repository, err := s.keywordSetRepository()
	if err != nil {
		return nil, err
	}

	return repository.ListKeywordSets(ctx, maximumKeywordSetList)
}

// SaveKeywordSet validates and stores one set, updating any set that already
// carries the same name so re-saving from the wizard never piles up copies.
func (s *Service) SaveKeywordSet(ctx context.Context, set KeywordSet) (KeywordSet, error) {
	repository, err := s.keywordSetRepository()
	if err != nil {
		return KeywordSet{}, err
	}

	set.Name = strings.TrimSpace(set.Name)
	if set.Name == "" || len(set.Name) > MaximumKeywordSetNameLength {
		return KeywordSet{}, fmt.Errorf(
			"%w: name must be 1 to %d characters", ErrInvalidKeywordSet, MaximumKeywordSetNameLength,
		)
	}

	if strings.IndexFunc(set.Name, unicode.IsControl) >= 0 {
		return KeywordSet{}, fmt.Errorf("%w: name contains control characters", ErrInvalidKeywordSet)
	}

	set.Description = strings.TrimSpace(set.Description)
	if len(set.Description) > MaximumKeywordSetDescriptionLength {
		return KeywordSet{}, fmt.Errorf(
			"%w: description must be at most %d characters",
			ErrInvalidKeywordSet, MaximumKeywordSetDescriptionLength,
		)
	}

	keywords, err := normalizeKeywordSetKeywords(set.Keywords)
	if err != nil {
		return KeywordSet{}, err
	}

	set.Keywords = keywords

	if set.ID == "" {
		set.ID = uuid.NewString()
	}

	now := time.Now().UTC()
	if set.CreatedAt.IsZero() {
		set.CreatedAt = now
	}

	set.UpdatedAt = now

	return repository.SaveKeywordSet(ctx, set)
}

// DeleteKeywordSet removes one saved set.
func (s *Service) DeleteKeywordSet(ctx context.Context, id string) error {
	repository, err := s.keywordSetRepository()
	if err != nil {
		return err
	}

	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("%w: a keyword set is required", ErrInvalidKeywordSet)
	}

	return repository.DeleteKeywordSet(ctx, id)
}

// UseKeywordSet records one wizard insertion and returns the stored keywords.
func (s *Service) UseKeywordSet(ctx context.Context, id string) (KeywordSet, error) {
	repository, err := s.keywordSetRepository()
	if err != nil {
		return KeywordSet{}, err
	}

	if strings.TrimSpace(id) == "" {
		return KeywordSet{}, fmt.Errorf("%w: a keyword set is required", ErrInvalidKeywordSet)
	}

	return repository.TouchKeywordSetUse(ctx, id, time.Now().UTC())
}

// normalizeKeywordSetKeywords trims lines, removes exact duplicates while
// keeping the first occurrence's order, and enforces the per-line bounds.
func normalizeKeywordSetKeywords(raw []string) ([]string, error) {
	keywords := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))

	for _, keyword := range raw {
		keyword = strings.TrimSpace(keyword)
		if keyword == "" {
			continue
		}

		if len(keyword) > MaximumKeywordSetKeywordLength {
			return nil, fmt.Errorf(
				"%w: each keyword must be at most %d characters",
				ErrInvalidKeywordSet, MaximumKeywordSetKeywordLength,
			)
		}

		if _, duplicate := seen[keyword]; duplicate {
			continue
		}

		seen[keyword] = struct{}{}
		keywords = append(keywords, keyword)
	}

	if len(keywords) == 0 {
		return nil, fmt.Errorf("%w: at least one keyword is required", ErrInvalidKeywordSet)
	}

	if len(keywords) > MaximumKeywordSetKeywords {
		return nil, fmt.Errorf(
			"%w: at most %d keywords are allowed", ErrInvalidKeywordSet, MaximumKeywordSetKeywords,
		)
	}

	return keywords, nil
}
