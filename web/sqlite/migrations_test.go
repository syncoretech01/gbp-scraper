package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

const legacyJobData = `{"keywords":["dentists in San Francisco"],"lang":"en","zoom":12,"lat":"37.7749","lon":"-122.4194","fast_mode":false,"radius":10000,"depth":10,"email":false,"extra_reviews":false,"max_time":600000000000,"proxies":null}`

func TestMigrateLegacyDatabasePreservesJobsCSVAndCreatesBackup(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	databasePath := filepath.Join(dir, "jobs.db")
	csvPath := filepath.Join(dir, "working-job.csv")
	csvContents := []byte("input_id,title,latitude,longitude\nsource-1,Dentist,37.77,-122.42\n")

	if err := os.WriteFile(csvPath, csvContents, 0o600); err != nil {
		t.Fatalf("write legacy CSV: %v", err)
	}

	legacy := openTestDatabase(t, databasePath)
	createLegacyJobsTable(t, legacy)

	statuses := []string{"pending", "working", "ok", "failed"}
	for i, status := range statuses {
		_, err := legacy.Exec(
			`INSERT INTO jobs(id, name, status, data, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			status+"-job",
			status+" name",
			status,
			legacyJobData,
			1000+i,
			2000+i,
		)
		if err != nil {
			t.Fatalf("insert legacy job %q: %v", status, err)
		}
	}

	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	db, err := initDatabase(databasePath)
	if err != nil {
		t.Fatalf("migrate legacy database: %v", err)
	}
	defer db.Close()

	for i, status := range statuses {
		var id, name, gotStatus, data string
		var createdAt, updatedAt int64
		err := db.QueryRow(
			`SELECT id, name, status, data, created_at, updated_at FROM jobs WHERE id = ?`,
			status+"-job",
		).Scan(&id, &name, &gotStatus, &data, &createdAt, &updatedAt)
		if err != nil {
			t.Fatalf("read migrated job %q: %v", status, err)
		}

		wantStatus := status
		if status == web.StatusWorking {
			// A process cannot still own work after this local process has
			// restarted. Migration 5 exposes it as safely paused/recoverable.
			wantStatus = web.StatusPending
		}
		if id != status+"-job" || name != status+" name" || gotStatus != wantStatus || data != legacyJobData ||
			createdAt != int64(1000+i) || status != web.StatusWorking && updatedAt != int64(2000+i) {
			t.Fatalf("legacy job changed: id=%q name=%q status=%q data=%q created=%d updated=%d",
				id, name, gotStatus, data, createdAt, updatedAt)
		}
	}

	columns, err := tableColumnNames(db, "jobs")
	if err != nil {
		t.Fatalf("read legacy jobs columns: %v", err)
	}
	if got, want := strings.Join(columns, ","), "id,name,status,data,created_at,updated_at"; got != want {
		t.Fatalf("legacy jobs columns = %q, want %q", got, want)
	}

	repository := &repo{db: db}
	workingJob, err := repository.Get(context.Background(), "working-job")
	if err != nil {
		t.Fatalf("read legacy job through repository: %v", err)
	}
	if workingJob.Status != web.StatusPending || workingJob.Data.MaxTime != 10*time.Minute ||
		workingJob.Data.Radius != 10000 || workingJob.Data.Lat != "37.7749" || workingJob.Data.Lon != "-122.4194" {
		t.Fatalf("legacy job decoded differently after migration: %#v", workingJob)
	}

	wantStates := map[string]string{
		"pending-job": "queued",
		"working-job": "paused",
		"ok-job":      "completed",
		"failed-job":  "failed",
	}
	for id, want := range wantStates {
		var state, configSnapshot string
		if err := db.QueryRow(
			`SELECT state, config_snapshot FROM job_runtime WHERE job_id = ?`,
			id,
		).Scan(&state, &configSnapshot); err != nil {
			t.Fatalf("read runtime for %q: %v", id, err)
		}

		if state != want {
			t.Fatalf("runtime state for %q = %q, want %q", id, state, want)
		}
		if configSnapshot != legacyJobData {
			t.Fatalf("runtime config snapshot for %q changed: %q", id, configSnapshot)
		}
		if id == "working-job" {
			var recoveryRequired int
			if err := db.QueryRow(
				`SELECT recovery_required FROM job_runtime WHERE job_id = ?`,
				id,
			).Scan(&recoveryRequired); err != nil {
				t.Fatalf("read recovery flag for %q: %v", id, err)
			}
			if recoveryRequired != 1 {
				t.Fatalf("recovery flag for %q = %d, want 1", id, recoveryRequired)
			}
		}

		var configuration string
		if err := db.QueryRow(
			`SELECT configuration FROM job_config_versions WHERE job_id = ? AND version = 1`,
			id,
		).Scan(&configuration); err != nil {
			t.Fatalf("read config version for %q: %v", id, err)
		}

		if configuration != legacyJobData {
			t.Fatalf("config version for %q changed: %q", id, configuration)
		}
	}

	gotCSV, err := os.ReadFile(csvPath)
	if err != nil {
		t.Fatalf("read legacy CSV after migration: %v", err)
	}
	if string(gotCSV) != string(csvContents) {
		t.Fatalf("legacy CSV changed during migration: got %q, want %q", gotCSV, csvContents)
	}

	backupFiles, err := filepath.Glob(filepath.Join(dir, "backups", "*.db"))
	if err != nil {
		t.Fatalf("list migration backups: %v", err)
	}
	if len(backupFiles) != 1 {
		t.Fatalf("migration backup count = %d, want 1 (%v)", len(backupFiles), backupFiles)
	}

	manifestFiles, err := filepath.Glob(filepath.Join(dir, "backups", "*.db.json"))
	if err != nil {
		t.Fatalf("list migration manifests: %v", err)
	}
	if len(manifestFiles) != 1 {
		t.Fatalf("migration manifest count = %d, want 1", len(manifestFiles))
	}

	manifestBytes, err := os.ReadFile(manifestFiles[0])
	if err != nil {
		t.Fatalf("read migration manifest: %v", err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("parse migration manifest: %v", err)
	}
	if manifest["sha256"] == "" || manifest["from_version"] != float64(0) ||
		manifest["to_version"] != float64(currentSchemaVersion) {
		t.Fatalf("unexpected migration manifest: %#v", manifest)
	}
	backupChecksum, backupSize, err := checksumFile(backupFiles[0])
	if err != nil {
		t.Fatalf("checksum migration backup: %v", err)
	}
	if manifest["sha256"] != backupChecksum || manifest["size_bytes"] != float64(backupSize) {
		t.Fatalf("migration manifest does not match backup: %#v", manifest)
	}

	backupDB := openTestDatabase(t, backupFiles[0])
	defer backupDB.Close()

	var backupData string
	if err := backupDB.QueryRow(`SELECT data FROM jobs WHERE id = 'working-job'`).Scan(&backupData); err != nil {
		t.Fatalf("read job from migration backup: %v", err)
	}
	if backupData != legacyJobData {
		t.Fatalf("backup job data changed: %q", backupData)
	}

	var backupRecords int
	if err := db.QueryRow(`SELECT COUNT(*) FROM backups WHERE kind = 'pre_migration' AND state = 'completed'`).Scan(&backupRecords); err != nil {
		t.Fatalf("count registered migration backups: %v", err)
	}
	if backupRecords != 1 {
		t.Fatalf("registered migration backups = %d, want 1", backupRecords)
	}

	reopened, err := initDatabase(databasePath)
	if err != nil {
		t.Fatalf("reopen migrated legacy database: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened legacy database: %v", err)
	}
	backupFiles, err = filepath.Glob(filepath.Join(dir, "backups", "*.db"))
	if err != nil {
		t.Fatalf("list migration backups after reopen: %v", err)
	}
	if len(backupFiles) != 1 {
		t.Fatalf("migration backup count after idempotent reopen = %d, want 1", len(backupFiles))
	}
}

func TestFreshDatabaseMigrationIsIdempotent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	databasePath := filepath.Join(dir, "jobs.db")

	db, err := initDatabase(databasePath)
	if err != nil {
		t.Fatalf("initialize database: %v", err)
	}

	assertCurrentSchema(t, db)

	var foreignKeys int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys pragma: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}

	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("read journal mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("journal mode = %q, want WAL", journalMode)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	db, err = initDatabase(databasePath)
	if err != nil {
		t.Fatalf("reopen migrated database: %v", err)
	}
	defer db.Close()

	assertCurrentSchema(t, db)

	backupFiles, err := filepath.Glob(filepath.Join(dir, "backups", "*.db"))
	if err != nil {
		t.Fatalf("list backups: %v", err)
	}
	if len(backupFiles) != 0 {
		t.Fatalf("fresh database unexpectedly created backups: %v", backupFiles)
	}
}

func TestMigrationRollbackIsAtomic(t *testing.T) {
	t.Parallel()

	db, err := initDatabase("file:migration-rollback?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("initialize in-memory database: %v", err)
	}
	defer db.Close()

	failing := schemaMigration{
		version: currentSchemaVersion + 1,
		name:    "deliberate-failure",
		statements: []string{
			`CREATE TABLE rollback_marker(id INTEGER PRIMARY KEY)`,
			`INSERT INTO rollback_marker(id) VALUES (1)`,
			`THIS IS NOT VALID SQL`,
		},
	}

	if err := applyMigration(db, failing); err == nil {
		t.Fatal("expected migration failure")
	}

	exists, err := tableExists(db, "rollback_marker")
	if err != nil {
		t.Fatalf("inspect rollback marker: %v", err)
	}
	if exists {
		t.Fatal("failed migration left its table behind")
	}

	version, err := schemaVersion(db)
	if err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("schema version after rollback = %d, want %d", version, currentSchemaVersion)
	}

	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`,
		failing.version,
	).Scan(&count); err != nil {
		t.Fatalf("query failed migration metadata: %v", err)
	}
	if count != 0 {
		t.Fatalf("failed migration recorded %d metadata rows", count)
	}
}

func TestFutureSchemaIsRejectedWithoutMutation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	databasePath := filepath.Join(dir, "future.db")
	db := openTestDatabase(t, databasePath)

	if _, err := db.Exec(`CREATE TABLE future_sentinel(value TEXT NOT NULL)`); err != nil {
		t.Fatalf("create future sentinel: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO future_sentinel(value) VALUES ('preserve-me')`); err != nil {
		t.Fatalf("insert future sentinel: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = " + testInt(currentSchemaVersion+1)); err != nil {
		t.Fatalf("set future schema version: %v", err)
	}
	var originalJournalMode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&originalJournalMode); err != nil {
		t.Fatalf("read original journal mode: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close future database: %v", err)
	}

	if _, err := initDatabase(databasePath); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("future database error = %v, want newer-than-supported error", err)
	}

	db = openTestDatabase(t, databasePath)
	defer db.Close()

	var value string
	if err := db.QueryRow(`SELECT value FROM future_sentinel`).Scan(&value); err != nil {
		t.Fatalf("read future sentinel: %v", err)
	}
	if value != "preserve-me" {
		t.Fatalf("future sentinel changed to %q", value)
	}

	var journalMode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read journal mode after rejection: %v", err)
	}
	if !strings.EqualFold(journalMode, originalJournalMode) {
		t.Fatalf("future database journal mode changed from %q to %q", originalJournalMode, journalMode)
	}

	exists, err := tableExists(db, "jobs")
	if err != nil {
		t.Fatalf("inspect jobs table: %v", err)
	}
	if exists {
		t.Fatal("future database was mutated with a jobs table")
	}

	if _, err := os.Stat(filepath.Join(dir, "backups")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("future database unexpectedly created backup directory: %v", err)
	}
}

func TestMigrationMetadataAndChecksumsAreValidated(t *testing.T) {
	t.Parallel()

	t.Run("missing metadata", func(t *testing.T) {
		databasePath := filepath.Join(t.TempDir(), "missing-metadata.db")
		db := openTestDatabase(t, databasePath)
		createLegacyJobsTable(t, db)
		if _, err := db.Exec(`PRAGMA user_version = 1`); err != nil {
			t.Fatalf("set schema version: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close database: %v", err)
		}

		if _, err := initDatabase(databasePath); err == nil || !strings.Contains(err.Error(), "metadata is missing") {
			t.Fatalf("metadata error = %v, want missing metadata error", err)
		}
	})

	t.Run("checksum mismatch", func(t *testing.T) {
		databasePath := filepath.Join(t.TempDir(), "checksum.db")
		db, err := initDatabase(databasePath)
		if err != nil {
			t.Fatalf("initialize database: %v", err)
		}

		if _, err := db.Exec(`UPDATE schema_migration_checksums SET checksum = 'tampered' WHERE version = 2`); err != nil {
			t.Fatalf("tamper checksum: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close database: %v", err)
		}

		if _, err := initDatabase(databasePath); err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("checksum error = %v, want mismatch error", err)
		}
	})

	t.Run("missing checksum row", func(t *testing.T) {
		databasePath := filepath.Join(t.TempDir(), "missing-checksum.db")
		db, err := initDatabase(databasePath)
		if err != nil {
			t.Fatalf("initialize database: %v", err)
		}

		if _, err := db.Exec(`DELETE FROM schema_migration_checksums WHERE version = 2`); err != nil {
			t.Fatalf("delete checksum: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close database: %v", err)
		}

		if _, err := initDatabase(databasePath); err == nil || !strings.Contains(err.Error(), "expected") {
			t.Fatalf("checksum error = %v, want incomplete-checksum error", err)
		}
	})

	t.Run("missing checksum table", func(t *testing.T) {
		databasePath := filepath.Join(t.TempDir(), "missing-checksum-table.db")
		db, err := initDatabase(databasePath)
		if err != nil {
			t.Fatalf("initialize database: %v", err)
		}

		if _, err := db.Exec(`DROP TABLE schema_migration_checksums`); err != nil {
			t.Fatalf("drop checksum table: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close database: %v", err)
		}

		if _, err := initDatabase(databasePath); err == nil || !strings.Contains(err.Error(), "metadata is missing") {
			t.Fatalf("checksum error = %v, want missing-checksum-table error", err)
		}
	})
}

func TestOnDiskMigrationFailureKeepsVersionThreeDataAndBackup(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	databasePath := filepath.Join(dir, "rollback.db")
	db := openTestDatabase(t, databasePath)
	createLegacyJobsTable(t, db)

	for _, migration := range schemaMigrations[:3] {
		if err := applyMigration(db, migration); err != nil {
			t.Fatalf("apply setup migration %d: %v", migration.version, err)
		}
	}

	if _, err := db.Exec(
		`INSERT INTO jobs(id, name, status, data, created_at, updated_at)
		VALUES ('preserved-job', 'Preserved', 'pending', ?, 10, 20)`,
		legacyJobData,
	); err != nil {
		t.Fatalf("insert job before failed migration: %v", err)
	}
	// Deliberately create a v4 column so migration 4 fails only after earlier
	// ALTER/CREATE statements have executed inside its transaction.
	if _, err := db.Exec(`ALTER TABLE job_tasks ADD COLUMN task_key TEXT NOT NULL DEFAULT ''`); err != nil {
		t.Fatalf("prepare migration conflict: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version-three database: %v", err)
	}

	if _, err := initDatabase(databasePath); err == nil || !strings.Contains(err.Error(), "duplicate column") {
		t.Fatalf("migration error = %v, want duplicate-column error", err)
	}

	db = openTestDatabase(t, databasePath)
	defer db.Close()

	version, err := schemaVersion(db)
	if err != nil {
		t.Fatalf("read schema version after failed migration: %v", err)
	}
	if version != 3 {
		t.Fatalf("schema version after failed migration = %d, want 3", version)
	}

	var data string
	if err := db.QueryRow(`SELECT data FROM jobs WHERE id = 'preserved-job'`).Scan(&data); err != nil {
		t.Fatalf("read job after failed migration: %v", err)
	}
	if data != legacyJobData {
		t.Fatalf("job data changed after failed migration: %q", data)
	}

	stateExists, err := columnExists(db, "job_runtime", "state")
	if err != nil {
		t.Fatalf("inspect rolled-back state column: %v", err)
	}
	if stateExists {
		t.Fatal("failed migration left job_runtime.state behind")
	}

	configExists, err := tableExists(db, "job_config_versions")
	if err != nil {
		t.Fatalf("inspect rolled-back config table: %v", err)
	}
	if configExists {
		t.Fatal("failed migration left job_config_versions behind")
	}

	backupFiles, err := filepath.Glob(filepath.Join(dir, "backups", "*.db"))
	if err != nil {
		t.Fatalf("list pre-migration backups: %v", err)
	}
	if len(backupFiles) != 1 {
		t.Fatalf("pre-migration backup count = %d, want 1", len(backupFiles))
	}
}

func TestVersionThreeDataMigratesWithoutLossAndAllowsRevertedSnapshot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	databasePath := filepath.Join(dir, "version-three.db")
	db := openTestDatabase(t, databasePath)
	createLegacyJobsTable(t, db)

	for _, migration := range schemaMigrations[:3] {
		if err := applyMigration(db, migration); err != nil {
			t.Fatalf("apply setup migration %d: %v", migration.version, err)
		}
	}

	if _, err := db.Exec(
		`INSERT INTO jobs(id, name, status, data, created_at, updated_at)
		VALUES ('job-1', 'Legacy', 'ok', ?, 10, 20)`,
		legacyJobData,
	); err != nil {
		t.Fatalf("insert version-three job: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO businesses(
			id, canonical_key, name, normalized_name, first_seen_at, last_seen_at,
			last_changed_at, created_at, updated_at
		) VALUES ('business-1', 'place:one', 'Original Dentist', 'original dentist', 10, 20, 20, 10, 20)`,
	); err != nil {
		t.Fatalf("insert version-three business: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO business_sources(
			business_id, job_id, source_type, source_url, source_query, source_cell,
			input_id, extraction_method, confidence, extracted_at
		) VALUES ('business-1', 'job-1', 'google_maps', 'https://maps.example/1', 'dentist', 'cell-1',
			'input-1', 'maps_detail', 0.9, 20)`,
	); err != nil {
		t.Fatalf("insert version-three source: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO business_versions(business_id, job_id, content_hash, change_type, snapshot, observed_at)
		VALUES ('business-1', 'job-1', 'hash-a', 'new', '{"name":"Original Dentist"}', 20)`,
	); err != nil {
		t.Fatalf("insert version-three version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close version-three database: %v", err)
	}

	db, err := initDatabase(databasePath)
	if err != nil {
		t.Fatalf("migrate version-three database: %v", err)
	}
	defer db.Close()

	var sourceURL, ingestKey string
	if err := db.QueryRow(
		`SELECT source_url, ingest_key FROM business_sources WHERE business_id = 'business-1'`,
	).Scan(&sourceURL, &ingestKey); err != nil {
		t.Fatalf("read migrated source: %v", err)
	}
	if sourceURL != "https://maps.example/1" || !strings.HasPrefix(ingestKey, "migrated-source:") {
		t.Fatalf("migrated source = url %q key %q", sourceURL, ingestKey)
	}

	var versionNo int
	var snapshot string
	if err := db.QueryRow(
		`SELECT version_no, snapshot FROM business_versions WHERE business_id = 'business-1'`,
	).Scan(&versionNo, &snapshot); err != nil {
		t.Fatalf("read migrated version: %v", err)
	}
	if versionNo != 1 || snapshot != `{"name":"Original Dentist"}` {
		t.Fatalf("migrated version = %d %q", versionNo, snapshot)
	}

	if _, err := db.Exec(
		`INSERT INTO business_versions(
			business_id, version_no, previous_version_id, job_id, content_hash,
			change_type, changed_fields, snapshot, observed_at
		) VALUES ('business-1', 2,
			(SELECT id FROM business_versions WHERE business_id = 'business-1' AND version_no = 1),
			'job-1', 'hash-a', 'changed', '["name"]', '{"name":"Original Dentist"}', 30)`,
	); err != nil {
		t.Fatalf("insert reverted snapshot with repeated content hash: %v", err)
	}
}

func TestBusinessFTSTriggersStaySynchronized(t *testing.T) {
	t.Parallel()

	db, err := initDatabase("file:fts-sync?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("initialize database: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(
		`INSERT INTO businesses(
			id, canonical_key, name, normalized_name, notes, first_seen_at, last_seen_at,
			last_changed_at, created_at, updated_at
		) VALUES ('business-1', 'place:one', 'Harbor Dentist', 'harbor dentist', 'family care', 1, 1, 1, 1, 1)`,
	)
	if err != nil {
		t.Fatalf("insert business: %v", err)
	}

	assertFTSCount(t, db, "Harbor", 1)

	if _, err := db.Exec(
		`UPDATE businesses SET name = 'Mission Dental', notes = 'orthodontics' WHERE id = 'business-1'`,
	); err != nil {
		t.Fatalf("update business: %v", err)
	}

	assertFTSCount(t, db, "Harbor", 0)
	assertFTSCount(t, db, "orthodontics", 1)

	if _, err := db.Exec(`DELETE FROM businesses WHERE id = 'business-1'`); err != nil {
		t.Fatalf("delete business: %v", err)
	}
	assertFTSCount(t, db, "orthodontics", 0)
}

func TestForeignKeysAreEnforced(t *testing.T) {
	t.Parallel()

	db, err := initDatabase("file:foreign-key-enforcement?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("initialize database: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(
		`INSERT INTO job_runtime(job_id, created_at, updated_at) VALUES ('missing-job', 1, 1)`,
	); err == nil {
		t.Fatal("orphan job_runtime row was accepted while foreign keys should be enabled")
	}

	if _, err := db.Exec(
		`INSERT INTO businesses(
			id, canonical_key, name, normalized_name, first_seen_at, last_seen_at,
			last_changed_at, created_at, updated_at
		) VALUES ('business-1', 'place:one', 'Dentist', 'dentist', 1, 1, 1, 1, 1)`,
	); err != nil {
		t.Fatalf("insert parent business: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO business_sources(
			business_id, job_id, source_type, extracted_at, ingest_key
		) VALUES ('business-1', 'missing-job', 'google_maps', 1, 'orphan-source')`,
	); err == nil {
		t.Fatal("business source with an unknown job was accepted")
	}
}

func TestRepositoryCreateAndUpdateMaintainFoundationRows(t *testing.T) {
	t.Parallel()

	db, err := initDatabase("file:repository-foundation?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("initialize database: %v", err)
	}
	defer db.Close()

	repository := &repo{db: db}
	job := web.Job{
		ID:     "job-1",
		Name:   "Dentists",
		Date:   time.Unix(100, 0).UTC(),
		Status: web.StatusPending,
		Data: web.JobData{
			Keywords: []string{"dentists"},
			Lang:     "en",
			Zoom:     12,
			Depth:    10,
			MaxTime:  10 * time.Minute,
		},
	}

	if err := repository.Create(context.Background(), &job); err != nil {
		t.Fatalf("create job: %v", err)
	}

	var state, configSnapshot, versionedConfig string
	if err := db.QueryRow(
		`SELECT state, config_snapshot FROM job_runtime WHERE job_id = ?`, job.ID,
	).Scan(&state, &configSnapshot); err != nil {
		t.Fatalf("read job runtime: %v", err)
	}
	if err := db.QueryRow(
		`SELECT configuration FROM job_config_versions WHERE job_id = ? AND version = 1`, job.ID,
	).Scan(&versionedConfig); err != nil {
		t.Fatalf("read versioned job config: %v", err)
	}
	if state != "queued" || configSnapshot == "" || versionedConfig != configSnapshot {
		t.Fatalf("unexpected foundation rows: state=%q snapshot=%q version=%q", state, configSnapshot, versionedConfig)
	}

	job.Status = web.StatusWorking
	if err := repository.Update(context.Background(), &job); err != nil {
		t.Fatalf("update job: %v", err)
	}
	if err := db.QueryRow(`SELECT state FROM job_runtime WHERE job_id = ?`, job.ID).Scan(&state); err != nil {
		t.Fatalf("read updated runtime: %v", err)
	}
	if state != "running" {
		t.Fatalf("runtime state after update = %q, want running", state)
	}

	if err := repository.Delete(context.Background(), job.ID); err != nil {
		t.Fatalf("delete job: %v", err)
	}
	var runtimeCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM job_runtime WHERE job_id = ?`, job.ID).Scan(&runtimeCount); err != nil {
		t.Fatalf("count runtime after delete: %v", err)
	}
	if runtimeCount != 0 {
		t.Fatalf("runtime rows after job delete = %d, want 0", runtimeCount)
	}
}

func openTestDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}

	return db
}

func createLegacyJobsTable(t *testing.T, db *sql.DB) {
	t.Helper()

	if _, err := db.Exec(`CREATE TABLE jobs (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		status TEXT NOT NULL,
		data TEXT NOT NULL,
		created_at INT NOT NULL,
		updated_at INT NOT NULL
	)`); err != nil {
		t.Fatalf("create legacy jobs table: %v", err)
	}
}

func columnExists(db *sql.DB, table, column string) (bool, error) {
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`,
		table,
		column,
	).Scan(&count)
	if err != nil {
		return false, err
	}

	return count == 1, nil
}

func tableColumnNames(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}

		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return columns, nil
}

func assertCurrentSchema(t *testing.T, db *sql.DB) {
	t.Helper()

	version, err := schemaVersion(db)
	if err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, currentSchemaVersion)
	}

	for _, table := range []string{
		"api_keys", "api_request_logs", "audit_logs", "backups",
		"business_changes", "business_identity_keys", "business_merges", "business_score_components",
		"business_sources", "business_tags", "business_versions", "businesses",
		"businesses_fts", "contact_evidence", "dedup_rules", "duplicate_candidates",
		"emails", "enrichment_tasks", "export_parts", "export_presets",
		"exports", "field_provenance", "integrations", "job_businesses",
		"job_checkpoints", "job_config_versions", "job_events", "job_logs",
		"job_progress", "job_runtime", "job_tags", "job_tasks",
		"jobs", "keyword_sets", "legacy_imports", "notes", "phones",
		"proxies", "proxy_health", "proxy_pool_members", "proxy_pools",
		"quality_rule_sets", "saved_areas", "saved_views", "schedule_runs",
		"schedules", "schema_migration_checksums", "schema_migrations", "settings",
		"social_profiles", "tags", "templates", "website_audit_pages",
		"website_audits", "website_detections", "websites",
	} {
		exists, err := tableExists(db, table)
		if err != nil {
			t.Fatalf("inspect table %q: %v", table, err)
		}
		if !exists {
			t.Errorf("required table %q is missing", table)
		}
	}

	var migrations, checksums int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrations); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migration_checksums`).Scan(&checksums); err != nil {
		t.Fatalf("count migration checksums: %v", err)
	}
	// Reserved version numbers may leave gaps in the declared sequence, so the
	// invariant is one recorded row and one checksum per declared migration.
	if migrations != len(schemaMigrations) || checksums != len(schemaMigrations) {
		t.Fatalf("migration rows = %d checksums = %d, want %d each", migrations, checksums, len(schemaMigrations))
	}

	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		t.Fatalf("run integrity check: %v", err)
	}
	if integrity != "ok" {
		t.Fatalf("integrity check = %q, want ok", integrity)
	}

	foreignKeyRows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("run foreign key check: %v", err)
	}
	defer foreignKeyRows.Close()
	if foreignKeyRows.Next() {
		var table string
		var rowID int64
		var parent string
		var constraint int
		if err := foreignKeyRows.Scan(&table, &rowID, &parent, &constraint); err != nil {
			t.Fatalf("read foreign key violation: %v", err)
		}
		t.Fatalf(
			"foreign key violation: table=%q rowid=%d parent=%q constraint=%d",
			table,
			rowID,
			parent,
			constraint,
		)
	}
	if err := foreignKeyRows.Err(); err != nil {
		t.Fatalf("run foreign key check: %v", err)
	}
}

func assertFTSCount(t *testing.T, db *sql.DB, query string, want int) {
	t.Helper()

	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM businesses_fts WHERE businesses_fts MATCH ?`,
		query,
	).Scan(&count); err != nil {
		t.Fatalf("search FTS for %q: %v", query, err)
	}
	if count != want {
		t.Fatalf("FTS count for %q = %d, want %d", query, count, want)
	}
}

func testInt(value int) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}

	var result [20]byte
	position := len(result)
	for value > 0 {
		position--
		result[position] = digits[value%10]
		value /= 10
	}

	return string(result[position:])
}
