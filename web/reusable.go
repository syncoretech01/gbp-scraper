package web

import (
	"context"
	"errors"
	"time"
)

var (
	ErrReusableStoreUnsupported = errors.New("saved configuration storage is unavailable")
	ErrReusableNotFound         = errors.New("saved configuration not found")
)

type ScrapeTemplate struct {
	ID            string
	Name          string
	Description   string
	Configuration JobData
	Tags          []string
	Folder        string
	Pinned        bool
	UseCount      int64
	LastRunAt     *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type SavedResultView struct {
	ID        string
	Name      string
	Search    ResultSearch
	CreatedAt time.Time
	UpdatedAt time.Time
}

type reusableRepository interface {
	ListScrapeTemplates(context.Context, string) ([]ScrapeTemplate, error)
	GetScrapeTemplate(context.Context, string) (ScrapeTemplate, error)
	SaveScrapeTemplate(context.Context, ScrapeTemplate) error
	DeleteScrapeTemplate(context.Context, string) error
	SetScrapeTemplatePinned(context.Context, string, bool) error
	RecordScrapeTemplateUse(context.Context, string, time.Time) error
	ListSavedResultViews(context.Context, string) ([]SavedResultView, error)
	GetSavedResultView(context.Context, string) (SavedResultView, error)
	SaveResultView(context.Context, SavedResultView) error
	DeleteSavedResultView(context.Context, string) error
}

func (s *Server) reusableAvailable() bool {
	if s == nil || s.svc == nil {
		return false
	}
	_, ok := s.svc.repo.(reusableRepository)
	return ok
}

func (s *Service) reusableRepository() (reusableRepository, error) {
	repository, ok := s.repo.(reusableRepository)
	if !ok {
		return nil, ErrReusableStoreUnsupported
	}
	return repository, nil
}

func (s *Service) ListScrapeTemplates(ctx context.Context, query string) ([]ScrapeTemplate, error) {
	repository, err := s.reusableRepository()
	if err != nil {
		return nil, err
	}
	return repository.ListScrapeTemplates(ctx, query)
}

func (s *Service) GetScrapeTemplate(ctx context.Context, id string) (ScrapeTemplate, error) {
	repository, err := s.reusableRepository()
	if err != nil {
		return ScrapeTemplate{}, err
	}
	return repository.GetScrapeTemplate(ctx, id)
}

func (s *Service) SaveScrapeTemplate(ctx context.Context, template ScrapeTemplate) error {
	repository, err := s.reusableRepository()
	if err != nil {
		return err
	}
	return repository.SaveScrapeTemplate(ctx, template)
}

func (s *Service) DeleteScrapeTemplate(ctx context.Context, id string) error {
	repository, err := s.reusableRepository()
	if err != nil {
		return err
	}
	return repository.DeleteScrapeTemplate(ctx, id)
}

func (s *Service) SetScrapeTemplatePinned(ctx context.Context, id string, pinned bool) error {
	repository, err := s.reusableRepository()
	if err != nil {
		return err
	}
	return repository.SetScrapeTemplatePinned(ctx, id, pinned)
}

func (s *Service) RecordScrapeTemplateUse(ctx context.Context, id string, usedAt time.Time) error {
	repository, err := s.reusableRepository()
	if err != nil {
		return err
	}
	return repository.RecordScrapeTemplateUse(ctx, id, usedAt)
}

func (s *Service) ListSavedResultViews(ctx context.Context, query string) ([]SavedResultView, error) {
	repository, err := s.reusableRepository()
	if err != nil {
		return nil, err
	}
	return repository.ListSavedResultViews(ctx, query)
}

func (s *Service) GetSavedResultView(ctx context.Context, id string) (SavedResultView, error) {
	repository, err := s.reusableRepository()
	if err != nil {
		return SavedResultView{}, err
	}
	return repository.GetSavedResultView(ctx, id)
}

func (s *Service) SaveResultView(ctx context.Context, view SavedResultView) error {
	repository, err := s.reusableRepository()
	if err != nil {
		return err
	}
	return repository.SaveResultView(ctx, view)
}

func (s *Service) DeleteSavedResultView(ctx context.Context, id string) error {
	repository, err := s.reusableRepository()
	if err != nil {
		return err
	}
	return repository.DeleteSavedResultView(ctx, id)
}
