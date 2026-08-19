package web

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// ErrRetentionUnsupported indicates the repository cannot apply retention.
var ErrRetentionUnsupported = errors.New("retention is unavailable")

// RetentionReport records what one retention pass removed. Retention only ever
// touches reproducible artifacts — manual backups beyond the configured count,
// version snapshots beyond their window, and, when a storage cap is exceeded,
// the oldest export files. Job result CSVs, the database, and pre-migration
// safety copies are never candidates.
type RetentionReport struct {
	BackupsPruned  int   `json:"backups_pruned"`
	VersionsPruned int64 `json:"versions_pruned"`
	ExportsPruned  int   `json:"exports_pruned"`
	BytesFreed     int64 `json:"bytes_freed"`
	StorageBytes   int64 `json:"storage_bytes"`
	CapBytes       int64 `json:"cap_bytes,omitempty"`
}

type retentionRepository interface {
	PruneManualBackups(context.Context, int) ([]BackupRecord, error)
	PruneBusinessVersions(context.Context, time.Time) (int64, error)
	OldestCompletedExports(context.Context, int) ([]ExportRecord, error)
}

// SupportsRetention reports whether retention policies can be applied.
func (s *Service) SupportsRetention() bool {
	_, ok := s.repo.(retentionRepository)

	return ok
}

// ApplyRetentionPolicies enforces the stored storage preferences once. It is
// safe to run repeatedly; a pass with nothing to remove changes nothing.
func (s *Service) ApplyRetentionPolicies(ctx context.Context) (RetentionReport, error) {
	repository, ok := s.repo.(retentionRepository)
	if !ok {
		return RetentionReport{}, ErrRetentionUnsupported
	}

	values, err := s.LoadSettings(ctx)
	if err != nil {
		return RetentionReport{}, fmt.Errorf("load retention settings: %w", err)
	}

	preferences := storagePreferencesFromMap(values)
	report := RetentionReport{}

	// Manual backups beyond the configured count. The files live under the
	// backups directory; rows are pruned first so a file that fails to unlink
	// can never resurrect a pruned row.
	if preferences.BackupCount > 0 {
		pruned, pruneErr := repository.PruneManualBackups(ctx, preferences.BackupCount)
		if pruneErr != nil {
			return report, pruneErr
		}

		report.BackupsPruned = len(pruned)

		for _, record := range pruned {
			if record.RelativePath == "" {
				continue
			}

			path, pathErr := safeDataPath(s.dataFolder, record.RelativePath)
			if pathErr != nil {
				continue
			}

			if info, statErr := os.Stat(path); statErr == nil {
				report.BytesFreed += info.Size()
			}

			if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return report, fmt.Errorf("remove pruned backup file: %w", removeErr)
			}
		}
	}

	// Version snapshots beyond the retention window; each business always
	// keeps its most recent snapshot.
	if preferences.VersionRetentionDays > 0 {
		cutoff := time.Now().UTC().AddDate(0, 0, -preferences.VersionRetentionDays)

		versions, pruneErr := repository.PruneBusinessVersions(ctx, cutoff)
		if pruneErr != nil {
			return report, pruneErr
		}

		report.VersionsPruned = versions
	}

	// Storage cap: free space by deleting the oldest completed exports until
	// the workspace fits. Exports are reproducible from the database, which is
	// what makes them the only safe thing to remove automatically.
	if preferences.MaximumStorageGB > 0 {
		report.CapBytes = int64(preferences.MaximumStorageGB) * (1 << 30)

		usage, usageErr := workspaceUsageBytes(s.dataFolder)
		if usageErr != nil {
			return report, usageErr
		}

		report.StorageBytes = usage

		if usage > report.CapBytes {
			oldest, listErr := repository.OldestCompletedExports(ctx, 100)
			if listErr != nil {
				return report, listErr
			}

			for _, record := range oldest {
				if usage <= report.CapBytes {
					break
				}

				if deleteErr := s.DeleteExport(ctx, record.ID); deleteErr != nil {
					return report, fmt.Errorf("prune export %s: %w", record.ID, deleteErr)
				}

				report.ExportsPruned++
				report.BytesFreed += record.FileSize
				usage -= record.FileSize
			}

			report.StorageBytes = usage
		}
	}

	return report, nil
}

// workspaceUsageBytes sums the size of every regular file in the data folder.
func workspaceUsageBytes(root string) (int64, error) {
	var total int64

	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// A file disappearing mid-walk is not an error worth failing on.
			return nil
		}

		if entry.Type().IsRegular() {
			if info, infoErr := entry.Info(); infoErr == nil {
				total += info.Size()
			}
		}

		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("measure workspace: %w", err)
	}

	return total, nil
}
