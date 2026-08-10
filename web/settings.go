package web

import (
	"context"
	"errors"
)

var ErrSettingsUnsupported = errors.New("local settings storage is unavailable")

type settingsRepository interface {
	LoadSettings(context.Context) (map[string]string, error)
	SaveSettings(context.Context, map[string]string) error
}

func (s *Service) LoadSettings(ctx context.Context) (map[string]string, error) {
	repository, ok := s.repo.(settingsRepository)
	if !ok {
		return nil, ErrSettingsUnsupported
	}
	return repository.LoadSettings(ctx)
}

func (s *Service) SaveSettings(ctx context.Context, values map[string]string) error {
	repository, ok := s.repo.(settingsRepository)
	if !ok {
		return ErrSettingsUnsupported
	}
	return repository.SaveSettings(ctx, values)
}
