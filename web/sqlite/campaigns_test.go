package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

func campaignTestJob(t *testing.T, repository *repo, id string) {
	t.Helper()

	job := lifecycleTestJob(id, time.Unix(1_700_000_000, 0).UTC())
	if err := repository.CreateWithState(context.Background(), &job, jobruntime.StateQueued); err != nil {
		t.Fatalf("create job %s: %v", id, err)
	}
}

func TestCampaignLineageRoundTripsAndOrdersByGeneration(t *testing.T) {
	t.Parallel()

	repository, closeDatabase := newLifecycleTestRepository(t, "campaign-lineage")
	defer closeDatabase()

	ctx := context.Background()
	for _, id := range []string{"root-job", "rescan-1", "rescan-2"} {
		campaignTestJob(t, repository, id)
	}

	created := time.Unix(1_700_000_100, 0).UTC()
	links := []web.JobCampaignLink{
		{JobID: "root-job", CampaignID: "root-job", RootJobID: "root-job", Generation: 0, CreatedAt: created},
		{
			JobID: "rescan-2", CampaignID: "root-job", RootJobID: "root-job",
			SourceJobID: "rescan-1", Mode: web.RerunModeChangedOnly, Generation: 2, CreatedAt: created,
		},
		{
			JobID: "rescan-1", CampaignID: "root-job", RootJobID: "root-job",
			SourceJobID: "root-job", Mode: web.RerunModeNewOnly, Generation: 1, CreatedAt: created,
		},
	}

	for _, link := range links {
		if err := repository.SaveJobCampaignLink(ctx, link); err != nil {
			t.Fatalf("save campaign link %s: %v", link.JobID, err)
		}
	}

	stored, err := repository.CampaignLinks(ctx, "root-job")
	if err != nil {
		t.Fatalf("list campaign links: %v", err)
	}

	if len(stored) != 3 {
		t.Fatalf("campaign links = %d, want 3", len(stored))
	}

	for index, want := range []string{"root-job", "rescan-1", "rescan-2"} {
		if stored[index].JobID != want {
			t.Fatalf("link %d = %q, want %q (generation order)", index, stored[index].JobID, want)
		}
	}

	single, err := repository.GetJobCampaignLink(ctx, "rescan-2")
	if err != nil {
		t.Fatalf("read campaign link: %v", err)
	}

	if single.SourceJobID != "rescan-1" || single.Mode != web.RerunModeChangedOnly || single.Generation != 2 {
		t.Fatalf("link = %#v", single)
	}

	if !single.CreatedAt.Equal(created) {
		t.Fatalf("link created at %s, want %s", single.CreatedAt, created)
	}

	// Re-saving is idempotent: the lineage is replaced, not duplicated.
	if err := repository.SaveJobCampaignLink(ctx, links[1]); err != nil {
		t.Fatalf("re-save campaign link: %v", err)
	}

	if again, err := repository.CampaignLinks(ctx, "root-job"); err != nil || len(again) != 3 {
		t.Fatalf("campaign links after re-save = %d (%v), want 3", len(again), err)
	}
}

func TestCampaignLineageReportsMissingLinks(t *testing.T) {
	t.Parallel()

	repository, closeDatabase := newLifecycleTestRepository(t, "campaign-missing")
	defer closeDatabase()

	ctx := context.Background()

	if _, err := repository.GetJobCampaignLink(ctx, "unknown-job"); !errors.Is(err, web.ErrCampaignNotFound) {
		t.Fatalf("missing lineage error = %v, want ErrCampaignNotFound", err)
	}

	if _, err := repository.FindCampaignIdempotencyKey(ctx, "campaign", ""); !errors.Is(err, web.ErrCampaignNotFound) {
		t.Fatalf("empty key error = %v, want ErrCampaignNotFound", err)
	}

	if _, err := repository.FindCampaignIdempotencyKey(ctx, "campaign", "nope"); !errors.Is(err, web.ErrCampaignNotFound) {
		t.Fatalf("unknown key error = %v, want ErrCampaignNotFound", err)
	}
}

func TestCampaignIdempotencyKeyIsUniquePerCampaign(t *testing.T) {
	t.Parallel()

	repository, closeDatabase := newLifecycleTestRepository(t, "campaign-idempotent")
	defer closeDatabase()

	ctx := context.Background()
	for _, id := range []string{"root-job", "rescan-1", "rescan-2"} {
		campaignTestJob(t, repository, id)
	}

	base := web.JobCampaignLink{
		CampaignID: "root-job", RootJobID: "root-job", SourceJobID: "root-job",
		Mode: web.RerunModeFull, Generation: 1, IdempotencyKey: "nightly-2026-08-21",
	}

	first := base
	first.JobID = "rescan-1"

	if err := repository.SaveJobCampaignLink(ctx, first); err != nil {
		t.Fatalf("save first rescan: %v", err)
	}

	found, err := repository.FindCampaignIdempotencyKey(ctx, "root-job", "nightly-2026-08-21")
	if err != nil || found.JobID != "rescan-1" {
		t.Fatalf("idempotent lookup = %#v (%v), want rescan-1", found, err)
	}

	// A second job may not claim the same key inside one campaign.
	second := base
	second.JobID = "rescan-2"

	if err := repository.SaveJobCampaignLink(ctx, second); err == nil {
		t.Fatal("a duplicate idempotency key was accepted inside one campaign")
	}

	// The same key in a different campaign is fine.
	other := base
	other.JobID = "rescan-2"
	other.CampaignID = "other-root"
	other.RootJobID = "other-root"

	if err := repository.SaveJobCampaignLink(ctx, other); err != nil {
		t.Fatalf("save link in another campaign: %v", err)
	}
}

func TestCampaignSnapshotsRoundTrip(t *testing.T) {
	t.Parallel()

	repository, closeDatabase := newLifecycleTestRepository(t, "benchmark-snapshots")
	defer closeDatabase()

	ctx := context.Background()
	campaignTestJob(t, repository, "snapshot-job")

	captured := time.Unix(1_700_000_500, 0).UTC()
	snapshot := web.BenchmarkSnapshot{
		JobID: "snapshot-job", JobName: "Dentists snapshot-job", CapturedAt: captured,
		EngineVersion: "v-test", SchemaVersion: currentSchemaVersion,
		UniqueBusinesses: 42, RowsAdded: 60, DuplicatesSkipped: 5, DuplicateRate: 0.0769,
		TasksCompleted: 8, TasksFailed: 1, TasksSkipped: 2, Retries: 3,
		WallSeconds: 120, NewBusinessesPerMinute: 21, Report: `{"job_id":"snapshot-job"}`,
	}

	if err := repository.SaveBenchmarkSnapshot(ctx, snapshot); err != nil {
		t.Fatalf("save benchmark snapshot: %v", err)
	}

	stored, err := repository.GetBenchmarkSnapshot(ctx, "snapshot-job")
	if err != nil {
		t.Fatalf("read benchmark snapshot: %v", err)
	}

	if stored.UniqueBusinesses != 42 || stored.Retries != 3 || stored.EngineVersion != "v-test" {
		t.Fatalf("snapshot = %#v", stored)
	}

	if !stored.CapturedAt.Equal(captured) {
		t.Fatalf("snapshot captured at %s, want %s", stored.CapturedAt, captured)
	}

	list, err := repository.ListBenchmarkSnapshots(ctx, 10)
	if err != nil || len(list) != 1 || list[0].JobID != "snapshot-job" {
		t.Fatalf("snapshot list = %#v (%v)", list, err)
	}

	// Re-capturing replaces the stored snapshot in place.
	snapshot.UniqueBusinesses = 55
	if err := repository.SaveBenchmarkSnapshot(ctx, snapshot); err != nil {
		t.Fatalf("re-save benchmark snapshot: %v", err)
	}

	again, err := repository.ListBenchmarkSnapshots(ctx, 10)
	if err != nil || len(again) != 1 || again[0].UniqueBusinesses != 55 {
		t.Fatalf("snapshot list after re-capture = %#v (%v)", again, err)
	}

	if _, err := repository.GetBenchmarkSnapshot(ctx, "no-such-job"); !errors.Is(err, web.ErrBenchmarkSnapshotNotFound) {
		t.Fatalf("missing snapshot error = %v, want ErrBenchmarkSnapshotNotFound", err)
	}
}
