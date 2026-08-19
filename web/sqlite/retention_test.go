package sqlite

import (
	"context"
	"testing"
	"time"
)

func TestPruneManualBackupsNeverTouchesMigrationCopies(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	now := time.Now().UTC().Unix()

	// Three manual backups and one pre-migration safety copy.
	for index, fixture := range []struct {
		id, kind, path string
		created        int64
	}{
		{"manual-old", "manual", "backups/manual-old.db", now - 300},
		{"manual-mid", "manual", "backups/manual-mid.db", now - 200},
		{"manual-new", "manual", "backups/manual-new.db", now - 100},
		{"migration-1", "pre_migration", "backups/jobs-schema-v0-to-v5.db", now - 400},
	} {
		if _, err := repository.db.ExecContext(ctx,
			`INSERT INTO backups(id, kind, state, relative_path, schema_version, created_at, finished_at)
			VALUES (?, ?, 'completed', ?, 9, ?, ?)`,
			fixture.id, fixture.kind, fixture.path, fixture.created, fixture.created,
		); err != nil {
			t.Fatalf("seed backup %d: %v", index, err)
		}
	}

	pruned, err := repository.PruneManualBackups(ctx, 2)
	if err != nil {
		t.Fatalf("PruneManualBackups: %v", err)
	}

	if len(pruned) != 1 || pruned[0].ID != "manual-old" {
		t.Fatalf("pruned = %+v, want exactly the oldest manual backup", pruned)
	}

	var manual, migration int
	if err := repository.db.QueryRowContext(ctx,
		`SELECT
			(SELECT COUNT(*) FROM backups WHERE kind = 'manual'),
			(SELECT COUNT(*) FROM backups WHERE kind = 'pre_migration')`,
	).Scan(&manual, &migration); err != nil {
		t.Fatalf("count backups: %v", err)
	}

	if manual != 2 {
		t.Fatalf("manual backups remaining = %d, want 2", manual)
	}

	if migration != 1 {
		t.Fatal("a pre-migration safety copy was pruned")
	}

	// The pass is audited.
	var audits int
	if err := repository.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_logs WHERE action = 'retention_applied' AND entity_type = 'backups'`,
	).Scan(&audits); err != nil {
		t.Fatalf("count audits: %v", err)
	}

	if audits != 1 {
		t.Fatalf("retention audits = %d, want 1", audits)
	}
}

func TestPruneBusinessVersionsKeepsEveryBusinessNewestSnapshot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	now := time.Now().UTC()
	old := now.AddDate(0, 0, -90).Unix()

	// A business whose snapshots are ALL old must still keep its newest one.
	if _, err := repository.db.ExecContext(ctx,
		`INSERT INTO businesses(
			id, canonical_key, name, normalized_name, last_changed_at,
			created_at, updated_at, first_seen_at, last_seen_at)
		VALUES
			('biz-a', 'key-a', 'Alpha', 'alpha', ?, ?, ?, ?, ?),
			('biz-b', 'key-b', 'Beta', 'beta', ?, ?, ?, ?, ?)`,
		old, old, old, old, old, old, old, old, old, old,
	); err != nil {
		t.Fatalf("seed businesses: %v", err)
	}

	for _, fixture := range []struct {
		business, hash string
		versionNo      int
		observed       int64
	}{
		{"biz-a", "hash-a1", 1, old},
		{"biz-a", "hash-a2", 2, old + 60},
		{"biz-b", "hash-b1", 1, old},
		{"biz-b", "hash-b2", 2, now.Unix()},
	} {
		if _, err := repository.db.ExecContext(ctx,
			`INSERT INTO business_versions(business_id, version_no, content_hash, change_type, snapshot, observed_at)
			VALUES (?, ?, ?, 'imported', '{}', ?)`,
			fixture.business, fixture.versionNo, fixture.hash, fixture.observed,
		); err != nil {
			t.Fatalf("seed version %s: %v", fixture.hash, err)
		}
	}

	pruned, err := repository.PruneBusinessVersions(ctx, now.AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("PruneBusinessVersions: %v", err)
	}

	// biz-a loses its older snapshot only; biz-b loses its old one only.
	if pruned != 2 {
		t.Fatalf("pruned = %d, want 2", pruned)
	}

	rows, err := repository.db.QueryContext(ctx,
		`SELECT business_id, content_hash FROM business_versions ORDER BY business_id, id`)
	if err != nil {
		t.Fatalf("read versions: %v", err)
	}

	defer func() { _ = rows.Close() }()

	remaining := map[string]string{}

	for rows.Next() {
		var business, hash string
		if err := rows.Scan(&business, &hash); err != nil {
			t.Fatalf("scan version: %v", err)
		}

		remaining[business] = hash
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("iterate versions: %v", err)
	}

	if remaining["biz-a"] != "hash-a2" || remaining["biz-b"] != "hash-b2" {
		t.Fatalf("remaining = %v, want each business's newest snapshot", remaining)
	}
}
