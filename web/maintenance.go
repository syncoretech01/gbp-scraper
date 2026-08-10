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

var ErrMaintenanceUnsupported = errors.New("local maintenance storage is unavailable")

type MaintenanceSnapshot struct {
	SchemaVersion int
	SQLiteVersion string
	Integrity     string
	DatabaseBytes int64
	JobCount      int64
	BusinessCount int64
	SourceCount   int64
	ExportCount   int64
	BackupCount   int64
}

type BackupRecord struct {
	ID            string
	Kind          string
	State         string
	RelativePath  string
	SchemaVersion int
	FileSize      int64
	Checksum      string
	CreatedAt     time.Time
	FinishedAt    *time.Time
	Error         string
}

type maintenanceRepository interface {
	MaintenanceSnapshot(context.Context) (MaintenanceSnapshot, error)
	RunIntegrityCheck(context.Context) (string, error)
	VacuumDatabase(context.Context) error
	CreateDatabaseBackup(context.Context) (BackupRecord, error)
	ListDatabaseBackups(context.Context, int) ([]BackupRecord, error)
	GetDatabaseBackup(context.Context, string) (BackupRecord, error)
}

func (s *Service) maintenanceRepository() (maintenanceRepository, error) {
	repository, ok := s.repo.(maintenanceRepository)
	if !ok {
		return nil, ErrMaintenanceUnsupported
	}

	return repository, nil
}

func (s *Service) MaintenanceSnapshot(ctx context.Context) (MaintenanceSnapshot, error) {
	repository, err := s.maintenanceRepository()
	if err != nil {
		return MaintenanceSnapshot{}, err
	}

	return repository.MaintenanceSnapshot(ctx)
}

func (s *Service) RunIntegrityCheck(ctx context.Context) (string, error) {
	repository, err := s.maintenanceRepository()
	if err != nil {
		return "", err
	}

	return repository.RunIntegrityCheck(ctx)
}

func (s *Service) VacuumDatabase(ctx context.Context) error {
	repository, err := s.maintenanceRepository()
	if err != nil {
		return err
	}

	return repository.VacuumDatabase(ctx)
}

func (s *Service) CreateDatabaseBackup(ctx context.Context) (BackupRecord, error) {
	repository, err := s.maintenanceRepository()
	if err != nil {
		return BackupRecord{}, err
	}

	return repository.CreateDatabaseBackup(ctx)
}

func (s *Service) ListDatabaseBackups(ctx context.Context, limit int) ([]BackupRecord, error) {
	repository, err := s.maintenanceRepository()
	if err != nil {
		return nil, err
	}

	return repository.ListDatabaseBackups(ctx, limit)
}

func (s *Service) GetDatabaseBackup(ctx context.Context, id string) (BackupRecord, string, error) {
	repository, err := s.maintenanceRepository()
	if err != nil {
		return BackupRecord{}, "", err
	}

	record, err := repository.GetDatabaseBackup(ctx, id)
	if err != nil {
		return BackupRecord{}, "", err
	}

	if record.State != "completed" || record.RelativePath == "" {
		return BackupRecord{}, "", fmt.Errorf("backup is not available")
	}

	clean := filepath.Clean(filepath.FromSlash(record.RelativePath))
	if filepath.IsAbs(clean) || clean == "." || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return BackupRecord{}, "", fmt.Errorf("invalid backup path")
	}

	base, err := filepath.Abs(s.dataFolder)
	if err != nil {
		return BackupRecord{}, "", err
	}
	full, err := filepath.Abs(filepath.Join(base, clean))
	if err != nil {
		return BackupRecord{}, "", err
	}
	relative, err := filepath.Rel(base, full)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return BackupRecord{}, "", fmt.Errorf("backup path escapes the data directory")
	}

	info, err := os.Stat(full)
	if err != nil || !info.Mode().IsRegular() {
		return BackupRecord{}, "", fmt.Errorf("backup file is unavailable")
	}

	return record, full, nil
}
