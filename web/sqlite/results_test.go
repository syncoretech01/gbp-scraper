package sqlite

import (
	"context"
	"encoding/csv"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/resultimport"
)

func TestLegacyResultImportIsIdempotentSearchableAndAuditable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, err := New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	concrete := repository.(*repo)
	t.Cleanup(func() { _ = concrete.db.Close() })

	job := resultImportJob("job-old", time.Unix(1_700_000_000, 0).UTC())
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	csvPath := filepath.Join(t.TempDir(), "job-old.csv")
	writeLegacyResultRows(t, csvPath,
		map[string]string{
			"input_id": "seed-a", "title": "Bay Smile Dental LLC", "category": "Dentist",
			"address":          "123 Main St, San Francisco, CA 94105, United States",
			"complete_address": `{"street":"123 Main St","city":"San Francisco","state":"California","postal_code":"94105","country":"US"}`,
			"website":          "https://www.baysmile.example/?utm_source=maps", "phone": "+1 415-555-0199",
			"emails": "INFO@BAYSMILE.EXAMPLE", "review_count": "42", "review_rating": "4.6",
			"latitude": "37.789", "longitude": "-122.394", "place_id": "place-bay-smile",
			"link": "https://www.google.com/maps/place/?q=place_id:place-bay-smile",
		},
		map[string]string{
			"input_id": "seed-b", "title": "Bay Smile Dental", "category": "Dentist",
			"address":          "123 Main St, San Francisco, CA 94105, United States",
			"complete_address": `{"street":"123 Main St","city":"San Francisco","state":"CA","postal_code":"94105","country":"US"}`,
			"website":          "https://baysmile.example", "phone": "(415) 555-0199",
			"emails": "info@baysmile.example", "review_count": "43", "review_rating": "4.7",
			"latitude": "37.789", "longitude": "-122.394", "place_id": "place-bay-smile",
			"link": "https://www.google.com/maps/place/?q=place_id:place-bay-smile",
		},
	)

	first, err := concrete.ImportLegacyCSV(ctx, job, csvPath)
	if err != nil {
		t.Fatalf("ImportLegacyCSV() error = %v", err)
	}
	if first.Rows != 2 || first.ImportedSources != 2 || first.UniqueBusinesses != 1 || first.Duplicates != 1 {
		t.Fatalf("ImportLegacyCSV() summary = %+v", first)
	}

	second, err := concrete.ImportLegacyCSV(ctx, job, csvPath)
	if err != nil {
		t.Fatalf("second ImportLegacyCSV() error = %v", err)
	}
	if !second.SkippedUnchanged || second.ImportedSources != 2 {
		t.Fatalf("second ImportLegacyCSV() summary = %+v", second)
	}

	page, err := concrete.SearchBusinesses(ctx, web.ResultSearch{
		Query: "Bay Smile",
		Filters: []web.ResultFilter{
			{Field: "city", Operator: "eq", Value: "San Francisco"},
			{Field: "rating", Operator: "gte", Value: "4.5"},
		},
		Limit: 25,
	})
	if err != nil {
		t.Fatalf("SearchBusinesses() error = %v", err)
	}
	if page.Total != 1 || len(page.Results) != 1 {
		t.Fatalf("SearchBusinesses() page = %+v", page)
	}
	if page.Results[0].PrimaryEmail != "info@baysmile.example" || page.Results[0].SourceQuery != "dentists in San Francisco" {
		t.Fatalf("SearchBusinesses() result = %+v", page.Results[0])
	}

	sources, err := concrete.SearchBusinesses(ctx, web.ResultSearch{IncludeDuplicates: true, Limit: 25})
	if err != nil {
		t.Fatalf("SearchBusinesses(include duplicates) error = %v", err)
	}
	if sources.Total != 2 || len(sources.Results) != 2 || sources.Results[0].SourceRecordID == 0 {
		t.Fatalf("SearchBusinesses(include duplicates) = %+v", sources)
	}

	detail, err := concrete.GetBusiness(ctx, page.Results[0].ID)
	if err != nil {
		t.Fatalf("GetBusiness() error = %v", err)
	}
	if len(detail.Sources) != 2 || len(detail.Versions) != 2 || detail.RawJSON == "" {
		t.Fatalf("GetBusiness() detail = %+v", detail)
	}

	overview, err := concrete.ResultOverview(ctx)
	if err != nil {
		t.Fatalf("ResultOverview() error = %v", err)
	}
	if overview.UniqueBusinesses != 1 || overview.RawRecords != 2 || overview.Websites != 1 {
		t.Fatalf("ResultOverview() = %+v", overview)
	}
}

func TestOlderImportCannotReplaceNewerPreferredValues(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, err := New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	concrete := repository.(*repo)
	t.Cleanup(func() { _ = concrete.db.Close() })

	newJob := resultImportJob("job-new", time.Unix(1_800_000_000, 0).UTC())
	oldJob := resultImportJob("job-old-late", time.Unix(1_600_000_000, 0).UTC())
	for _, job := range []*web.Job{&newJob, &oldJob} {
		if err := repository.Create(ctx, job); err != nil {
			t.Fatalf("Create(%s) error = %v", job.ID, err)
		}
	}

	newPath := filepath.Join(t.TempDir(), "new.csv")
	oldPath := filepath.Join(t.TempDir(), "old.csv")
	writeLegacyResultRows(t, newPath, map[string]string{
		"title": "Newest Dental Name", "category": "Dentist", "address": "1 Market St, San Francisco, CA 94105, United States",
		"place_id": "stable-place", "review_rating": "4.9", "website": "https://newest.example",
	})
	writeLegacyResultRows(t, oldPath, map[string]string{
		"title": "Old Dental Name", "category": "Dentist", "address": "1 Market St, San Francisco, CA 94105, United States",
		"place_id": "stable-place", "review_rating": "2.1", "website": "https://old.example",
	})
	if _, err := concrete.ImportLegacyCSV(ctx, newJob, newPath); err != nil {
		t.Fatalf("new ImportLegacyCSV() error = %v", err)
	}
	if _, err := concrete.ImportLegacyCSV(ctx, oldJob, oldPath); err != nil {
		t.Fatalf("old ImportLegacyCSV() error = %v", err)
	}

	page, err := concrete.SearchBusinesses(ctx, web.ResultSearch{Limit: 10})
	if err != nil {
		t.Fatalf("SearchBusinesses() error = %v", err)
	}
	if len(page.Results) != 1 || page.Results[0].Name != "Newest Dental Name" || page.Results[0].Rating == nil || *page.Results[0].Rating != 4.9 {
		t.Fatalf("preferred result = %+v", page.Results)
	}

	var preferredName, preferredWebsite int
	if err := concrete.db.QueryRowContext(ctx,
		`SELECT
			SUM(CASE WHEN field_name = 'name' AND preferred = 1 AND superseded_at IS NULL THEN 1 ELSE 0 END),
			SUM(CASE WHEN field_name = 'website' AND preferred = 1 AND superseded_at IS NULL THEN 1 ELSE 0 END)
		FROM field_provenance WHERE business_id = ? AND extracted_at = ?`,
		page.Results[0].ID,
		newJob.Date.Unix(),
	).Scan(&preferredName, &preferredWebsite); err != nil {
		t.Fatalf("read preferred provenance: %v", err)
	}
	if preferredName != 1 || preferredWebsite != 1 {
		t.Fatalf("preferred provenance name=%d website=%d", preferredName, preferredWebsite)
	}
}

func TestFuzzyDuplicatesBecomeReviewCandidatesWithoutAutomaticMerge(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, err := New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	concrete := repository.(*repo)
	t.Cleanup(func() { _ = concrete.db.Close() })
	job := resultImportJob("job-fuzzy", time.Unix(1_800_000_000, 0).UTC())
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "fuzzy.csv")
	writeLegacyResultRows(t, path,
		map[string]string{
			"title": "Golden Gate Dental Care", "category": "Dentist",
			"address":          "100 Market St, San Francisco, CA 94105, United States",
			"complete_address": `{"street":"100 Market St","city":"San Francisco","state":"CA","postal_code":"94105","country":"US"}`,
			"latitude":         "37.79360", "longitude": "-122.39580", "place_id": "place-golden-one",
		},
		map[string]string{
			"title": "Golden Gate Dental Centre", "category": "Dental clinic",
			"address":          "102 Market Street, San Francisco, CA 94105, United States",
			"complete_address": `{"street":"102 Market Street","city":"San Francisco","state":"CA","postal_code":"94105","country":"US"}`,
			"latitude":         "37.79375", "longitude": "-122.39572", "place_id": "place-golden-two",
		},
		map[string]string{
			"title": "Unrelated Coffee", "category": "Cafe",
			"address":          "110 Market Street, San Francisco, CA 94105, United States",
			"complete_address": `{"street":"110 Market Street","city":"San Francisco","state":"CA","postal_code":"94105","country":"US"}`,
			"latitude":         "37.79380", "longitude": "-122.39570", "place_id": "place-coffee",
		},
	)
	summary, err := concrete.ImportLegacyCSV(ctx, job, path)
	if err != nil {
		t.Fatalf("ImportLegacyCSV() error = %v", err)
	}
	if summary.UniqueBusinesses != 3 || summary.Duplicates != 0 {
		t.Fatalf("import summary = %+v", summary)
	}

	var candidates int
	var score float64
	var signals string
	if err := concrete.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MAX(score), 0), COALESCE(MAX(signals), '')
		FROM duplicate_candidates WHERE state = 'pending'`,
	).Scan(&candidates, &score, &signals); err != nil {
		t.Fatalf("read duplicate candidates: %v", err)
	}
	if candidates != 1 || score < 0.62 || !strings.Contains(signals, "distance_metres") {
		t.Fatalf("duplicate candidates = %d, score = %.3f, signals = %s", candidates, score, signals)
	}
	var merged int
	if err := concrete.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM businesses WHERE merged_into_id IS NOT NULL").Scan(&merged); err != nil {
		t.Fatalf("count merged businesses: %v", err)
	}
	if merged != 0 {
		t.Fatalf("fuzzy matching automatically merged %d businesses", merged)
	}
}

func TestBusinessWorkflowMutationsAreSearchableAndAudited(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, err := New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	concrete := repository.(*repo)
	t.Cleanup(func() { _ = concrete.db.Close() })
	job := resultImportJob("job-workflow", time.Unix(1_800_000_000, 0).UTC())
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "workflow.csv")
	writeLegacyResultRows(t, path, map[string]string{
		"title": "Workflow Dental", "category": "Dentist", "place_id": "workflow-place",
		"address": "1 Market St, San Francisco, CA 94105, United States",
	})
	if _, err := concrete.ImportLegacyCSV(ctx, job, path); err != nil {
		t.Fatalf("ImportLegacyCSV() error = %v", err)
	}
	page, err := concrete.SearchBusinesses(ctx, web.ResultSearch{Limit: 10})
	if err != nil || len(page.Results) != 1 {
		t.Fatalf("SearchBusinesses() = %+v, %v", page, err)
	}
	id := page.Results[0].ID
	for _, mutation := range []web.ResultMutation{
		{IDs: []string{id, id}, Action: "tag", Value: "Priority"},
		{IDs: []string{id}, Action: "reviewed"},
		{IDs: []string{id}, Action: "notes", Value: "Call after 9 AM"},
	} {
		changed, err := concrete.MutateBusinesses(ctx, mutation)
		if err != nil || changed != 1 {
			t.Fatalf("MutateBusinesses(%+v) = %d, %v", mutation, changed, err)
		}
	}
	detail, err := concrete.GetBusiness(ctx, id)
	if err != nil {
		t.Fatalf("GetBusiness() error = %v", err)
	}
	if !detail.Business.Reviewed || detail.Business.Notes != "Call after 9 AM" ||
		len(detail.Business.Tags) != 1 || detail.Business.Tags[0] != "Priority" {
		t.Fatalf("business workflow = %+v", detail.Business)
	}
	tagged, err := concrete.SearchBusinesses(ctx, web.ResultSearch{
		Filters: []web.ResultFilter{{Field: "tags", Operator: "contains", Value: "Priority"}}, Limit: 10,
	})
	if err != nil || tagged.Total != 1 {
		t.Fatalf("tag search = %+v, %v", tagged, err)
	}
	var notes, audits int
	if err := concrete.db.QueryRowContext(ctx,
		`SELECT
			(SELECT COUNT(*) FROM notes WHERE entity_type = 'business' AND entity_id = ?),
			(SELECT COUNT(*) FROM audit_logs WHERE action = 'business_workflow_updated')`,
		id,
	).Scan(&notes, &audits); err != nil {
		t.Fatalf("read workflow history: %v", err)
	}
	if notes != 1 || audits != 3 {
		t.Fatalf("workflow notes = %d, audits = %d", notes, audits)
	}
	if _, err := concrete.MutateBusinesses(ctx, web.ResultMutation{IDs: []string{id}, Action: "untag", Value: "priority"}); err != nil {
		t.Fatalf("untag error = %v", err)
	}
	if _, err := concrete.MutateBusinesses(ctx, web.ResultMutation{IDs: []string{id}, Action: "delete"}); !errors.Is(err, web.ErrInvalidResultMutation) {
		t.Fatalf("unsupported mutation error = %v", err)
	}
}

func TestMalformedImportRollsBackNormalizedRows(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, err := New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	concrete := repository.(*repo)
	t.Cleanup(func() { _ = concrete.db.Close() })
	job := resultImportJob("job-bad", time.Unix(1_700_000_000, 0).UTC())
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "bad.csv")
	if err := os.WriteFile(path, []byte("title,place_id\n\"unterminated"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := concrete.ImportLegacyCSV(ctx, job, path); err == nil || !errors.Is(err, resultimport.ErrMalformedCSV) {
		t.Fatalf("ImportLegacyCSV() error = %v, want ErrMalformedCSV", err)
	}

	var businesses, sources, imports int
	if err := concrete.db.QueryRowContext(ctx,
		`SELECT (SELECT COUNT(*) FROM businesses), (SELECT COUNT(*) FROM business_sources),
			(SELECT COUNT(*) FROM legacy_imports WHERE job_id = ?)`, job.ID,
	).Scan(&businesses, &sources, &imports); err != nil {
		t.Fatalf("read rollback counts: %v", err)
	}
	if businesses != 0 || sources != 0 || imports != 1 {
		t.Fatalf("rollback counts businesses=%d sources=%d imports=%d", businesses, sources, imports)
	}
	var state, failure string
	if err := concrete.db.QueryRowContext(ctx,
		`SELECT state, error FROM legacy_imports WHERE job_id = ?`, job.ID,
	).Scan(&state, &failure); err != nil {
		t.Fatalf("read failed import state: %v", err)
	}
	if state != "failed" || failure != "malformed CSV" {
		t.Fatalf("failed import state=%q error=%q", state, failure)
	}
}

func resultImportJob(id string, date time.Time) web.Job {
	return web.Job{
		ID: id, Name: id, Date: date, Status: web.StatusOK,
		Data: web.JobData{
			Keywords: []string{"dentists in San Francisco"}, Lang: "en", Zoom: 15,
			Depth: 10, MaxTime: 30 * time.Minute,
		},
	}
}

func writeLegacyResultRows(t *testing.T, path string, records ...map[string]string) {
	t.Helper()

	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(%s) error = %v", path, err)
	}
	writer := csv.NewWriter(file)
	headers := resultimport.LegacyHeaders()
	if err := writer.Write(headers); err != nil {
		t.Fatalf("write CSV header: %v", err)
	}
	for _, record := range records {
		row := make([]string, len(headers))
		for index, header := range headers {
			row[index] = record[header]
		}
		if err := writer.Write(row); err != nil {
			t.Fatalf("write CSV row: %v", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		t.Fatalf("flush CSV: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close CSV: %v", err)
	}
}
