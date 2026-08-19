package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

func TestSettingsAndExportRegistryPersistAuditableState(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()
	ctx := context.Background()

	if err := repository.SaveSettings(ctx, map[string]string{"theme": "dark", "default_language": "en"}); err != nil {
		t.Fatalf("SaveSettings() error = %v", err)
	}
	if err := repository.SaveSettings(ctx, map[string]string{"theme": "light"}); err != nil {
		t.Fatalf("second SaveSettings() error = %v", err)
	}
	settings, err := repository.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("LoadSettings() error = %v", err)
	}
	if settings["theme"] != "light" || settings["default_language"] != "en" {
		t.Fatalf("settings = %#v", settings)
	}
	var version, auditCount int
	if err := repository.db.QueryRow("SELECT version FROM settings WHERE key = 'theme'").Scan(&version); err != nil {
		t.Fatalf("read setting version: %v", err)
	}
	if err := repository.db.QueryRow("SELECT COUNT(*) FROM audit_logs WHERE action = 'settings_updated'").Scan(&auditCount); err != nil {
		t.Fatalf("read settings audit count: %v", err)
	}
	if version != 2 || auditCount != 2 {
		t.Fatalf("version = %d, audit count = %d", version, auditCount)
	}

	now := time.Unix(1_800_000_000, 0).UTC()
	record := web.ExportRecord{
		ID: "export-one", Name: "Dentists", Format: "csv", State: "running",
		SourceType: "results", Filters: `{"query":"dentist"}`, Columns: `["name"]`,
		CreatedAt: now, StartedAt: &now,
	}
	if err := repository.CreateExport(ctx, record); err != nil {
		t.Fatalf("CreateExport() error = %v", err)
	}
	finished := now.Add(time.Second)
	record.State = "completed"
	record.RelativePath = "exports/export-one.csv"
	record.RecordCount = 36
	record.FileSize = 1024
	record.Checksum = strings.Repeat("a", 64)
	record.FinishedAt = &finished
	if err := repository.UpdateExport(ctx, record); err != nil {
		t.Fatalf("UpdateExport() error = %v", err)
	}
	got, err := repository.GetExport(ctx, record.ID)
	if err != nil {
		t.Fatalf("GetExport() error = %v", err)
	}
	if got.State != "completed" || got.RecordCount != 36 || got.RelativePath != record.RelativePath || got.FinishedAt == nil {
		t.Fatalf("export = %+v", got)
	}
	if err := repository.DeleteExport(ctx, record.ID); err != nil {
		t.Fatalf("DeleteExport() error = %v", err)
	}
	if _, err := repository.GetExport(ctx, record.ID); !errors.Is(err, web.ErrExportNotFound) {
		t.Fatalf("GetExport() after delete error = %v", err)
	}
}

func TestManualBackupIsVerifiedChecksummedAndRegistered(t *testing.T) {
	t.Parallel()

	repository, dataDirectory, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()
	ctx := context.Background()
	job := lifecycleTestJob("backup-job", time.Now().UTC())
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	record, err := repository.CreateDatabaseBackup(ctx)
	if err != nil {
		t.Fatalf("CreateDatabaseBackup() error = %v", err)
	}
	backupPath := filepath.Join(dataDirectory, filepath.FromSlash(record.RelativePath))
	if err := verifySQLiteDatabase(backupPath); err != nil {
		t.Fatalf("verifySQLiteDatabase() error = %v", err)
	}
	checksum, size, err := checksumFile(backupPath)
	if err != nil {
		t.Fatalf("checksumFile() error = %v", err)
	}
	if record.State != "completed" || record.SchemaVersion != currentSchemaVersion ||
		record.Checksum != checksum || record.FileSize != size || record.FinishedAt == nil {
		t.Fatalf("backup record = %+v; checksum=%q size=%d", record, checksum, size)
	}
	backups, err := repository.ListDatabaseBackups(ctx, 10)
	if err != nil {
		t.Fatalf("ListDatabaseBackups() error = %v", err)
	}
	if len(backups) != 1 || backups[0].ID != record.ID {
		t.Fatalf("backups = %+v", backups)
	}
	var jobs int
	backupDB := openTestDatabase(t, backupPath)
	if err := backupDB.QueryRow("SELECT COUNT(*) FROM jobs WHERE id = 'backup-job'").Scan(&jobs); err != nil {
		t.Fatalf("read backup job: %v", err)
	}
	if err := backupDB.Close(); err != nil {
		t.Fatalf("close backup database: %v", err)
	}
	if jobs != 1 {
		t.Fatalf("backup jobs = %d, want 1", jobs)
	}
}

func TestReusableConfigurationsAndDueScheduleSurviveQueueing(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	template := web.ScrapeTemplate{
		ID: "template-one", Name: "San Francisco dentists", Tags: []string{"dental", "sf"},
		Folder: "Local", Configuration: validScheduledJobData(), CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.SaveScrapeTemplate(ctx, template); err != nil {
		t.Fatalf("SaveScrapeTemplate() error = %v", err)
	}
	if err := repository.SetScrapeTemplatePinned(ctx, template.ID, true); err != nil {
		t.Fatalf("SetScrapeTemplatePinned() error = %v", err)
	}
	if err := repository.RecordScrapeTemplateUse(ctx, template.ID, now.Add(time.Minute)); err != nil {
		t.Fatalf("RecordScrapeTemplateUse() error = %v", err)
	}
	gotTemplate, err := repository.GetScrapeTemplate(ctx, template.ID)
	if err != nil {
		t.Fatalf("GetScrapeTemplate() error = %v", err)
	}
	if !gotTemplate.Pinned || gotTemplate.UseCount != 1 || gotTemplate.LastRunAt == nil ||
		len(gotTemplate.Configuration.Keywords) != 1 {
		t.Fatalf("template = %+v", gotTemplate)
	}

	view := web.SavedResultView{
		ID: "view-one", Name: "High-rated SF dentists",
		Search: web.ResultSearch{Query: "dentist", Sort: "rating_desc", Filters: []web.ResultFilter{
			{Field: "city", Operator: "eq", Value: "San Francisco"},
			{Field: "rating", Operator: "gte", Value: "4.5"},
		}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.SaveResultView(ctx, view); err != nil {
		t.Fatalf("SaveResultView() error = %v", err)
	}
	gotView, err := repository.GetSavedResultView(ctx, view.ID)
	if err != nil {
		t.Fatalf("GetSavedResultView() error = %v", err)
	}
	if gotView.Search.Query != "dentist" || len(gotView.Search.Filters) != 2 {
		t.Fatalf("saved view = %+v", gotView)
	}

	due := now.Add(-time.Minute)
	schedule := web.ScheduleRecord{
		ID: "schedule-one", Name: "Weekly dentists", TemplateID: template.ID, Timezone: "America/Los_Angeles",
		Enabled: true, Spec: web.ScheduleSpec{
			Recurrence: "once", FirstRunAt: due, OverlapPolicy: "queue", MissedPolicy: "run_once",
		},
		NextRunAt: &due, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	if err := repository.SaveSchedule(ctx, schedule); err != nil {
		t.Fatalf("SaveSchedule() error = %v", err)
	}
	jobs, err := repository.StartDueSchedules(ctx, now, 10)
	if err != nil {
		t.Fatalf("StartDueSchedules() error = %v", err)
	}
	if len(jobs) != 1 || jobs[0].Data.GridCellKM != 2.5 {
		t.Fatalf("scheduled jobs = %+v", jobs)
	}
	second, err := repository.StartDueSchedules(ctx, now.Add(time.Second), 10)
	if err != nil || len(second) != 0 {
		t.Fatalf("second StartDueSchedules() = %+v, %v", second, err)
	}
	schedules, err := repository.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("ListSchedules() error = %v", err)
	}
	if len(schedules) != 1 || schedules[0].Enabled || schedules[0].NextRunAt != nil {
		t.Fatalf("schedules = %+v", schedules)
	}
	runs, err := repository.ListScheduleRuns(ctx, 10)
	if err != nil {
		t.Fatalf("ListScheduleRuns() error = %v", err)
	}
	if len(runs) != 1 || runs[0].State != string(jobruntime.StateQueued) || runs[0].JobID != jobs[0].ID {
		t.Fatalf("schedule runs = %+v", runs)
	}
	if _, err := repository.db.Exec("UPDATE job_runtime SET state = 'running', started_at = ? WHERE job_id = ?", now.Unix(), jobs[0].ID); err != nil {
		t.Fatalf("advance scheduled runtime: %v", err)
	}
	runs, err = repository.ListScheduleRuns(ctx, 10)
	if err != nil || len(runs) != 1 || runs[0].State != string(jobruntime.StateRunning) {
		t.Fatalf("live schedule runs = %+v, %v", runs, err)
	}
}

func TestInvalidDueScheduleIsQuarantinedWithoutBlockingValidWork(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()
	ctx := context.Background()
	now := time.Unix(1_800_100_000, 0).UTC()
	due := now.Add(-30 * time.Second)

	validTemplate := web.ScrapeTemplate{
		ID: "template-valid", Name: "Valid dentists", Configuration: validScheduledJobData(),
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	removedTemplate := web.ScrapeTemplate{
		ID: "template-removed", Name: "Removed dentists", Configuration: validScheduledJobData(),
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	for _, template := range []web.ScrapeTemplate{validTemplate, removedTemplate} {
		if err := repository.SaveScrapeTemplate(ctx, template); err != nil {
			t.Fatalf("SaveScrapeTemplate(%s) error = %v", template.ID, err)
		}
	}

	spec := web.ScheduleSpec{
		Recurrence: "once", FirstRunAt: due, OverlapPolicy: "queue", MissedPolicy: "run_once",
	}
	for _, schedule := range []web.ScheduleRecord{
		{
			ID: "a-invalid", Name: "Missing template", TemplateID: removedTemplate.ID,
			Timezone: "UTC", Enabled: true, Spec: spec, NextRunAt: &due,
			CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
		},
		{
			ID: "z-valid", Name: "Valid schedule", TemplateID: validTemplate.ID,
			Timezone: "UTC", Enabled: true, Spec: spec, NextRunAt: &due,
			CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
		},
	} {
		if err := repository.SaveSchedule(ctx, schedule); err != nil {
			t.Fatalf("SaveSchedule(%s) error = %v", schedule.ID, err)
		}
	}
	if err := repository.DeleteScrapeTemplate(ctx, removedTemplate.ID); err != nil {
		t.Fatalf("DeleteScrapeTemplate() error = %v", err)
	}

	jobs, err := repository.StartDueSchedules(ctx, now, 10)
	if err != nil {
		t.Fatalf("StartDueSchedules() error = %v", err)
	}
	if len(jobs) != 1 || !strings.HasPrefix(jobs[0].Name, "Valid schedule") {
		t.Fatalf("queued jobs = %+v", jobs)
	}

	schedules, err := repository.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("ListSchedules() error = %v", err)
	}
	for _, schedule := range schedules {
		if schedule.Enabled || schedule.NextRunAt != nil {
			t.Errorf("schedule %s was not advanced/disabled: %+v", schedule.ID, schedule)
		}
	}

	runs, err := repository.ListScheduleRuns(ctx, 10)
	if err != nil {
		t.Fatalf("ListScheduleRuns() error = %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("schedule runs = %+v", runs)
	}
	states := make(map[string]web.ScheduleRunRecord, len(runs))
	for _, run := range runs {
		states[run.ScheduleID] = run
	}
	failed := states["a-invalid"]
	if failed.State != "failed" || failed.FinishedAt == nil || failed.Error == "" || failed.JobID != "" {
		t.Errorf("quarantined run = %+v", failed)
	}
	queued := states["z-valid"]
	if queued.State != string(jobruntime.StateQueued) || queued.StartedAt != nil || queued.JobID == "" {
		t.Errorf("queued run = %+v", queued)
	}
	var quarantineAudits int
	if err := repository.db.QueryRow(
		"SELECT COUNT(*) FROM audit_logs WHERE action = 'schedule_quarantined' AND entity_id = 'a-invalid'",
	).Scan(&quarantineAudits); err != nil {
		t.Fatalf("read quarantine audit: %v", err)
	}
	if quarantineAudits != 1 {
		t.Fatalf("quarantine audits = %d, want 1", quarantineAudits)
	}
}

func TestProxySecretsAreEncryptedDeduplicatedAndPersistent(t *testing.T) {
	t.Parallel()

	repository, dataDirectory, closeRepository := newLocalFeatureRepository(t)
	ctx := context.Background()
	secret := "http://alice:correct-horse-battery-staple@127.0.0.1:8080" // #nosec G101 -- synthetic proxy credential used only to verify encryption at rest.
	pool, imported, err := repository.ImportProxyPool(ctx, "Primary", "fastest", []string{secret, "  " + secret + "  "})
	if err != nil {
		closeRepository()
		t.Fatalf("ImportProxyPool() error = %v", err)
	}
	if imported != 1 || pool.TotalCount != 1 {
		closeRepository()
		t.Fatalf("pool = %+v, imported = %d", pool, imported)
	}
	_, imported, err = repository.ImportProxyPool(ctx, "Primary", "fastest", []string{secret})
	if err != nil || imported != 0 {
		closeRepository()
		t.Fatalf("duplicate import = %d, %v", imported, err)
	}
	proxies, err := repository.ListProxies(ctx, pool.ID)
	if err != nil || len(proxies) != 1 {
		closeRepository()
		t.Fatalf("ListProxies() = %+v, %v", proxies, err)
	}
	if strings.Contains(proxies[0].MaskedURL, "correct-horse") || !strings.Contains(proxies[0].MaskedURL, "REDACTED") {
		closeRepository()
		t.Fatalf("masked URL = %q", proxies[0].MaskedURL)
	}
	var encrypted, masked string
	if err := repository.db.QueryRow("SELECT url_encrypted, url_masked FROM proxies WHERE id = ?", proxies[0].ID).Scan(&encrypted, &masked); err != nil {
		closeRepository()
		t.Fatalf("read encrypted proxy: %v", err)
	}
	if strings.Contains(encrypted, "correct-horse") || strings.Contains(masked, "correct-horse") {
		closeRepository()
		t.Fatal("proxy password was stored in plaintext")
	}
	key, err := os.ReadFile(filepath.Join(dataDirectory, proxyKeyFilename))
	if err != nil || len(key) != 32 {
		closeRepository()
		t.Fatalf("proxy key length = %d, error = %v", len(key), err)
	}
	resolved, err := repository.ResolveProxyPool(ctx, pool.ID)
	if err != nil || len(resolved) != 1 || resolved[0] != secret {
		closeRepository()
		t.Fatalf("ResolveProxyPool() = %#v, %v", resolved, err)
	}
	proxyID := proxies[0].ID
	closeRepository()

	reopenedRaw, err := New(filepath.Join(dataDirectory, "jobs.db"))
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	reopened := reopenedRaw.(*repo)
	defer func() { _ = reopened.db.Close() }()
	recovered, err := reopened.GetProxySecret(ctx, proxyID)
	if err != nil || recovered != secret {
		t.Fatalf("GetProxySecret() after restart = %q, %v", recovered, err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		if err := reopened.RecordProxyTest(ctx, proxyID, web.ProxyTestResult{
			Status: "offline", Error: "proxy " + secret + " failed", CheckedAt: time.Now().UTC().Add(time.Duration(attempt) * time.Second),
		}); err != nil {
			t.Fatalf("RecordProxyTest(%d) error = %v", attempt, err)
		}
	}
	proxies, err = reopened.ListProxies(ctx, pool.ID)
	if err != nil || len(proxies) != 1 || proxies[0].Enabled {
		t.Fatalf("failed proxy = %+v, %v", proxies, err)
	}
	var loggedError string
	if err := reopened.db.QueryRow("SELECT error FROM proxy_health ORDER BY id DESC LIMIT 1").Scan(&loggedError); err != nil {
		t.Fatalf("read proxy health error: %v", err)
	}
	if strings.Contains(loggedError, "correct-horse") {
		t.Fatalf("proxy health log leaked credentials: %q", loggedError)
	}
}

func newLocalFeatureRepository(t *testing.T) (*repo, string, func()) {
	t.Helper()
	dataDirectory := t.TempDir()
	repository, err := New(filepath.Join(dataDirectory, "jobs.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	concrete := repository.(*repo)
	return concrete, dataDirectory, func() {
		if err := concrete.db.Close(); err != nil {
			t.Errorf("close repository: %v", err)
		}
	}
}

func validScheduledJobData() web.JobData {
	return web.JobData{
		Keywords: []string{"dentists in San Francisco"}, Lang: "en", Zoom: 12,
		Lat: "37.7749", Lon: "-122.4194", LocationLabel: "San Francisco",
		Radius: 10000, Depth: 10, MaxTime: 90 * time.Minute,
		GridBBox: "37.708,-122.515,37.833,-122.354", GridCellKM: 2.5,
	}
}
