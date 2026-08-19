package web

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// fakeRetentionRepository drives ApplyRetentionPolicies without SQLite so the
// file-handling and cap arithmetic are the subject under test. The SQL side is
// covered separately in the sqlite package.
type fakeRetentionRepository struct {
	fixedJobRepository

	settings map[string]string

	prunableBackups []BackupRecord
	backupKeep      int

	versionCutoff  time.Time
	versionsPruned int64

	oldestExports  []ExportRecord
	deletedExports []string
}

func (r *fakeRetentionRepository) LoadSettings(context.Context) (map[string]string, error) {
	return r.settings, nil
}

func (r *fakeRetentionRepository) SaveSettings(context.Context, map[string]string) error {
	return nil
}

func (r *fakeRetentionRepository) PruneManualBackups(_ context.Context, keep int) ([]BackupRecord, error) {
	r.backupKeep = keep

	return r.prunableBackups, nil
}

func (r *fakeRetentionRepository) PruneBusinessVersions(_ context.Context, cutoff time.Time) (int64, error) {
	r.versionCutoff = cutoff

	return r.versionsPruned, nil
}

func (r *fakeRetentionRepository) OldestCompletedExports(context.Context, int) ([]ExportRecord, error) {
	return r.oldestExports, nil
}

func (r *fakeRetentionRepository) GetExport(_ context.Context, id string) (ExportRecord, error) {
	for _, record := range r.oldestExports {
		if record.ID == id {
			return record, nil
		}
	}

	return ExportRecord{}, ErrExportNotFound
}

func (r *fakeRetentionRepository) ListExports(context.Context, int) ([]ExportRecord, error) {
	return r.oldestExports, nil
}

func (r *fakeRetentionRepository) CreateExport(context.Context, ExportRecord) error { return nil }
func (r *fakeRetentionRepository) UpdateExport(context.Context, ExportRecord) error { return nil }

func (r *fakeRetentionRepository) DeleteExport(_ context.Context, id string) error {
	r.deletedExports = append(r.deletedExports, id)

	return nil
}

func TestApplyRetentionPrunesBackupFilesAndVersions(t *testing.T) {
	t.Parallel()

	dataFolder := t.TempDir()

	backupsDir := filepath.Join(dataFolder, "backups")
	if err := os.MkdirAll(backupsDir, 0o700); err != nil {
		t.Fatalf("create backups dir: %v", err)
	}

	prunedFile := filepath.Join(backupsDir, "old-manual.db")
	if err := os.WriteFile(prunedFile, []byte("old backup bytes"), 0o600); err != nil {
		t.Fatalf("write backup fixture: %v", err)
	}

	// A pre-migration safety copy sits in the same directory. Retention must
	// leave it alone: the repository only ever returns manual backups, and the
	// service must only unlink what the repository returned.
	migrationCopy := filepath.Join(backupsDir, "jobs-schema-v0-to-v5.db")
	if err := os.WriteFile(migrationCopy, []byte("migration safety copy"), 0o600); err != nil {
		t.Fatalf("write migration fixture: %v", err)
	}

	repository := &fakeRetentionRepository{
		settings: map[string]string{
			"storage.backup_count":           "3",
			"storage.version_retention_days": "30",
		},
		prunableBackups: []BackupRecord{{ID: "b-old", RelativePath: "backups/old-manual.db"}},
		versionsPruned:  7,
	}

	service := NewService(repository, dataFolder)

	report, err := service.ApplyRetentionPolicies(context.Background())
	if err != nil {
		t.Fatalf("ApplyRetentionPolicies: %v", err)
	}

	if report.BackupsPruned != 1 || report.VersionsPruned != 7 {
		t.Fatalf("report = %+v", report)
	}

	if repository.backupKeep != 3 {
		t.Fatalf("backup keep = %d, want 3", repository.backupKeep)
	}

	if _, statErr := os.Stat(prunedFile); !os.IsNotExist(statErr) {
		t.Fatal("pruned backup file still exists")
	}

	if _, statErr := os.Stat(migrationCopy); statErr != nil {
		t.Fatalf("migration safety copy was touched: %v", statErr)
	}

	// The version cutoff honours the configured window.
	expected := time.Now().UTC().AddDate(0, 0, -30)
	if diff := repository.versionCutoff.Sub(expected); diff < -time.Minute || diff > time.Minute {
		t.Fatalf("version cutoff = %v, want about %v", repository.versionCutoff, expected)
	}
}

func TestApplyRetentionEnforcesStorageCapOnOldestExports(t *testing.T) {
	t.Parallel()

	dataFolder := t.TempDir()

	exportsDir := filepath.Join(dataFolder, "exports")
	if err := os.MkdirAll(exportsDir, 0o700); err != nil {
		t.Fatalf("create exports dir: %v", err)
	}

	for index := range 3 {
		path := filepath.Join(exportsDir, "export-"+strconv.Itoa(index)+".csv")
		if err := os.WriteFile(path, make([]byte, 4096), 0o600); err != nil {
			t.Fatalf("write export fixture: %v", err)
		}
	}

	repository := &fakeRetentionRepository{
		settings: map[string]string{"storage.maximum_gb": "1"},
		oldestExports: []ExportRecord{
			{ID: "e-oldest", RelativePath: "exports/export-0.csv", FileSize: 4096},
			{ID: "e-middle", RelativePath: "exports/export-1.csv", FileSize: 4096},
			{ID: "e-newest", RelativePath: "exports/export-2.csv", FileSize: 4096},
		},
	}

	service := NewService(repository, dataFolder)

	// Under the cap: nothing is deleted.
	report, err := service.ApplyRetentionPolicies(context.Background())
	if err != nil {
		t.Fatalf("ApplyRetentionPolicies under cap: %v", err)
	}

	if report.ExportsPruned != 0 || len(repository.deletedExports) != 0 {
		t.Fatalf("under-cap pass deleted exports: %+v", report)
	}
}

func TestApplyRetentionDeletesOldestExportsFirstWhenOverCap(t *testing.T) {
	t.Parallel()

	dataFolder := t.TempDir()

	exportsDir := filepath.Join(dataFolder, "exports")
	if err := os.MkdirAll(exportsDir, 0o700); err != nil {
		t.Fatalf("create exports dir: %v", err)
	}

	// Three 600 KB exports with a 1 MB cap: the pass must delete the two
	// oldest and stop once the workspace fits.
	const size = 600 << 10

	records := make([]ExportRecord, 0, 3)

	for index := range 3 {
		name := "export-" + strconv.Itoa(index) + ".csv"
		if err := os.WriteFile(filepath.Join(exportsDir, name), make([]byte, size), 0o600); err != nil {
			t.Fatalf("write export fixture: %v", err)
		}

		records = append(records, ExportRecord{
			ID: "e-" + strconv.Itoa(index), RelativePath: "exports/" + name, FileSize: size,
		})
	}

	repository := &fakeRetentionRepository{
		settings:      map[string]string{"storage.maximum_gb": "1"},
		oldestExports: records,
	}

	// A sparse file makes the workspace look over-cap without writing 1 GB.
	filler := filepath.Join(dataFolder, "filler.bin")

	file, err := os.Create(filler)
	if err != nil {
		t.Fatalf("create filler: %v", err)
	}

	if err := file.Truncate(1<<30 + 1<<20); err != nil {
		_ = file.Close()
		t.Skip("filesystem does not support sparse truncate")
	}

	if err := file.Close(); err != nil {
		t.Fatalf("close filler: %v", err)
	}

	service := NewService(repository, dataFolder)

	report, err := service.ApplyRetentionPolicies(context.Background())
	if err != nil {
		t.Fatalf("ApplyRetentionPolicies over cap: %v", err)
	}

	if report.ExportsPruned == 0 {
		t.Fatalf("over-cap pass deleted nothing: %+v", report)
	}

	if len(repository.deletedExports) == 0 || repository.deletedExports[0] != "e-0" {
		t.Fatalf("deletion order = %v, want oldest first", repository.deletedExports)
	}
}
