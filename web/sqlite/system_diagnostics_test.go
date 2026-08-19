package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

func TestSystemDatabaseSnapshotAndRollbackOnlyWriteProbe(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()
	ctx := context.Background()
	job := lifecycleTestJob("system-diagnostic-job", time.Now().UTC())
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	snapshot, err := repository.SystemDatabaseSnapshot(ctx)
	if err != nil {
		t.Fatalf("SystemDatabaseSnapshot() error = %v", err)
	}
	if snapshot.SchemaVersion != currentSchemaVersion || snapshot.SQLiteVersion == "" ||
		snapshot.DatabaseBytes <= 0 || snapshot.JobCount != 1 || snapshot.QueuedJobs != 1 ||
		snapshot.RunningJobs != 0 || snapshot.LastWriteAt == nil {
		t.Fatalf("snapshot = %+v", snapshot)
	}

	var auditsBefore int
	if err := repository.db.QueryRow("SELECT COUNT(*) FROM audit_logs").Scan(&auditsBefore); err != nil {
		t.Fatalf("count audit rows before probe: %v", err)
	}
	if err := repository.CheckDatabaseWritable(ctx); err != nil {
		t.Fatalf("CheckDatabaseWritable() error = %v", err)
	}
	var auditsAfter int
	if err := repository.db.QueryRow("SELECT COUNT(*) FROM audit_logs").Scan(&auditsAfter); err != nil {
		t.Fatalf("count audit rows after probe: %v", err)
	}
	if auditsAfter != auditsBefore {
		t.Fatalf("rollback-only write probe persisted an audit row: before=%d after=%d", auditsBefore, auditsAfter)
	}

	var _ interface {
		SystemDatabaseSnapshot(context.Context) (web.SystemDatabaseSnapshot, error)
		CheckDatabaseWritable(context.Context) error
	} = repository
}
