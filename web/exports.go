package web

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrExportStoreUnsupported = errors.New("local export storage is unavailable")
	ErrExportNotFound         = errors.New("export not found")
)

type ExportRecord struct {
	ID           string
	Name         string
	Format       string
	State        string
	SourceType   string
	SourceID     string
	Filters      string
	Columns      string
	RelativePath string
	RecordCount  int64
	FileSize     int64
	Checksum     string
	Error        string
	CreatedAt    time.Time
	StartedAt    *time.Time
	FinishedAt   *time.Time
}

type exportRepository interface {
	CreateExport(context.Context, ExportRecord) error
	UpdateExport(context.Context, ExportRecord) error
	ListExports(context.Context, int) ([]ExportRecord, error)
	GetExport(context.Context, string) (ExportRecord, error)
	DeleteExport(context.Context, string) error
}

func (s *Server) exportAvailable() bool {
	if s == nil || s.svc == nil {
		return false
	}
	_, ok := s.svc.repo.(exportRepository)
	return ok
}

func (s *Service) exportRepository() (exportRepository, error) {
	repository, ok := s.repo.(exportRepository)
	if !ok {
		return nil, ErrExportStoreUnsupported
	}
	return repository, nil
}

func (s *Service) CreateExport(ctx context.Context, record ExportRecord) error {
	repository, err := s.exportRepository()
	if err != nil {
		return err
	}
	return repository.CreateExport(ctx, record)
}

func (s *Service) UpdateExport(ctx context.Context, record ExportRecord) error {
	repository, err := s.exportRepository()
	if err != nil {
		return err
	}
	return repository.UpdateExport(ctx, record)
}

func (s *Service) ListExports(ctx context.Context, limit int) ([]ExportRecord, error) {
	repository, err := s.exportRepository()
	if err != nil {
		return nil, err
	}
	return repository.ListExports(ctx, limit)
}

func (s *Service) GetExport(ctx context.Context, id string) (ExportRecord, string, error) {
	repository, err := s.exportRepository()
	if err != nil {
		return ExportRecord{}, "", err
	}
	record, err := repository.GetExport(ctx, id)
	if err != nil {
		return ExportRecord{}, "", err
	}
	if record.State != "completed" || record.RelativePath == "" {
		return ExportRecord{}, "", fmt.Errorf("export is not available")
	}
	full, err := safeDataPath(s.dataFolder, record.RelativePath)
	if err != nil {
		return ExportRecord{}, "", err
	}
	info, err := os.Stat(full)
	if err != nil || !info.Mode().IsRegular() {
		return ExportRecord{}, "", fmt.Errorf("export file is unavailable")
	}
	return record, full, nil
}

func (s *Service) DeleteExport(ctx context.Context, id string) error {
	repository, err := s.exportRepository()
	if err != nil {
		return err
	}
	record, err := repository.GetExport(ctx, id)
	if err != nil {
		return err
	}
	if record.RelativePath != "" {
		path, pathErr := safeDataPath(s.dataFolder, record.RelativePath)
		if pathErr != nil {
			return pathErr
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return removeErr
		}
	}
	return repository.DeleteExport(ctx, id)
}

func safeDataPath(dataFolder, relativePath string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(relativePath))
	if filepath.IsAbs(clean) || clean == "." || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid local data path")
	}
	base, err := filepath.Abs(dataFolder)
	if err != nil {
		return "", err
	}
	full, err := filepath.Abs(filepath.Join(base, clean))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(base, full)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("local data path escapes the data directory")
	}
	return full, nil
}
