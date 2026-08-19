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
	PresetID     string
	SavedViewID  string
	Filters      string
	Columns      string
	Options      string
	RelativePath string
	RecordCount  int64
	FileSize     int64
	Checksum     string
	Error        string
	CreatedAt    time.Time
	StartedAt    *time.Time
	FinishedAt   *time.Time
}

// ExportPreset stores a repeatable delivery shape and source query. Presets
// contain no generated file paths, credentials, or other machine secrets.
type ExportPreset struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Format    string    `json:"format"`
	Columns   string    `json:"columns"`
	Filters   string    `json:"filters"`
	Sort      string    `json:"sort"`
	Options   string    `json:"options"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ExportPart records an independently verified file that belongs to an
// export. Multipart deliveries are also packaged into the parent ZIP file.
type ExportPart struct {
	ExportID     string `json:"export_id"`
	PartNumber   int    `json:"part_number"`
	RelativePath string `json:"relative_path"`
	RecordCount  int64  `json:"record_count"`
	FileSize     int64  `json:"file_size"`
	Checksum     string `json:"checksum"`
}

type exportRepository interface {
	CreateExport(context.Context, ExportRecord) error
	UpdateExport(context.Context, ExportRecord) error
	ListExports(context.Context, int) ([]ExportRecord, error)
	GetExport(context.Context, string) (ExportRecord, error)
	DeleteExport(context.Context, string) error
}

// richExportRepository is deliberately additive so embedders implementing
// the original export registry keep working. The bundled SQLite repository
// implements this interface and enables presets and multipart audit records.
type richExportRepository interface {
	SaveExportPreset(context.Context, ExportPreset) (ExportPreset, error)
	ListExportPresets(context.Context, int) ([]ExportPreset, error)
	GetExportPreset(context.Context, string) (ExportPreset, error)
	DeleteExportPreset(context.Context, string) error
	ReplaceExportParts(context.Context, string, []ExportPart) error
	ListExportParts(context.Context, string) ([]ExportPart, error)
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

// SaveExportPreset creates or replaces a named local export preset.
func (s *Service) SaveExportPreset(ctx context.Context, preset ExportPreset) (ExportPreset, error) {
	repository, ok := s.repo.(richExportRepository)
	if !ok {
		return ExportPreset{}, ErrExportStoreUnsupported
	}
	return repository.SaveExportPreset(ctx, preset)
}

// ListExportPresets returns recent repeatable delivery definitions.
func (s *Service) ListExportPresets(ctx context.Context, limit int) ([]ExportPreset, error) {
	repository, ok := s.repo.(richExportRepository)
	if !ok {
		return nil, ErrExportStoreUnsupported
	}
	return repository.ListExportPresets(ctx, limit)
}

// GetExportPreset returns one repeatable delivery definition.
func (s *Service) GetExportPreset(ctx context.Context, id string) (ExportPreset, error) {
	repository, ok := s.repo.(richExportRepository)
	if !ok {
		return ExportPreset{}, ErrExportStoreUnsupported
	}
	return repository.GetExportPreset(ctx, id)
}

// DeleteExportPreset removes a delivery definition without touching earlier
// generated exports.
func (s *Service) DeleteExportPreset(ctx context.Context, id string) error {
	repository, ok := s.repo.(richExportRepository)
	if !ok {
		return ErrExportStoreUnsupported
	}
	return repository.DeleteExportPreset(ctx, id)
}

// ReplaceExportParts atomically replaces the verified part manifest.
func (s *Service) ReplaceExportParts(ctx context.Context, id string, parts []ExportPart) error {
	repository, ok := s.repo.(richExportRepository)
	if !ok {
		if len(parts) <= 1 {
			return nil
		}
		return ErrExportStoreUnsupported
	}
	return repository.ReplaceExportParts(ctx, id, parts)
}

// ListExportParts returns the verified files belonging to an export.
func (s *Service) ListExportParts(ctx context.Context, id string) ([]ExportPart, error) {
	repository, ok := s.repo.(richExportRepository)
	if !ok {
		return nil, ErrExportStoreUnsupported
	}
	return repository.ListExportParts(ctx, id)
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
	if richRepository, ok := s.repo.(richExportRepository); ok {
		parts, partsErr := richRepository.ListExportParts(ctx, id)
		if partsErr != nil {
			return partsErr
		}
		for _, part := range parts {
			if part.RelativePath == "" || part.RelativePath == record.RelativePath {
				continue
			}
			path, pathErr := safeDataPath(s.dataFolder, part.RelativePath)
			if pathErr != nil {
				return pathErr
			}
			if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return removeErr
			}
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
