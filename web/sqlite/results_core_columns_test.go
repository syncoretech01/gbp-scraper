package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

// The Results explorer promises the specification's complete core column set.
// These tests pin the parts that only the repository can supply: the columns
// read straight from the imported row, and the ones assembled from the child
// tables (emails, phones, social profiles, website audits).

func TestSearchBusinessesReturnsTheSpecificationCoreColumns(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	job := resultImportJob("job-core-columns", time.Now().UTC())
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatalf("create job: %v", err)
	}

	path := filepath.Join(t.TempDir(), "core.csv")
	writeLegacyResultRows(t, path, map[string]string{
		"input_id":           "seed-42",
		"title":              "Bay Smile Dental",
		"category":           "Dentist",
		"address":            "123 Market St, San Francisco, CA 94105, United States",
		"plus_code":          "849VQH48+92",
		"descriptions":       "Family dentistry in SoMa.",
		"phone":              "+1 415 555 0100",
		"website":            "https://baysmile.example",
		"emails":             "hello@baysmile.example",
		"review_count":       "128",
		"review_rating":      "4.7",
		"reviews_per_rating": `{"5":100,"4":20,"3":5,"2":2,"1":1}`,
		"popular_times":      `{"monday":[1,2,3],"tuesday":[2,3,4]}`,
		"user_reviews":       `[{"Name":"A"},{"Name":"B"}]`,
		"place_id":           "core-1",
		"latitude":           "37.7749",
		"longitude":          "-122.4194",
	})

	if _, err := repository.ImportLegacyCSV(ctx, job, path); err != nil {
		t.Fatalf("import core columns: %v", err)
	}

	page, err := repository.SearchBusinesses(ctx, web.ResultSearch{Limit: 5})
	if err != nil {
		t.Fatalf("SearchBusinesses() error = %v", err)
	}

	if len(page.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(page.Results))
	}

	result := page.Results[0]
	for _, check := range []struct {
		name string
		got  string
		want string
	}{
		{"input id", result.InputID, "seed-42"},
		{"description", result.Description, "Family dentistry in SoMa."},
		{"plus code", result.PlusCode, "849VQH48+92"},
		{"street", result.Street, "123 Market St"},
	} {
		if check.got != check.want {
			t.Errorf("%s = %q, want %q", check.name, check.got, check.want)
		}
	}

	if !strings.Contains(result.ReviewsPerRating, `"5"`) {
		t.Errorf("ratings breakdown = %q, want the stored per-rating counts", result.ReviewsPerRating)
	}

	if !strings.Contains(result.PopularTimes, "monday") {
		t.Errorf("popular times = %q, want the stored day profile", result.PopularTimes)
	}

	if !strings.Contains(result.UserReviews, `"Name":"A"`) {
		t.Errorf("user reviews = %q, want the stored review array", result.UserReviews)
	}

	if len(result.Emails) != 1 || result.Emails[0] != "hello@baysmile.example" {
		t.Errorf("emails = %v, want the extracted address", result.Emails)
	}

	if result.EmailStatus == "" {
		t.Error("email status is empty, want the stored classification")
	}

	if result.FirstSeenAt.IsZero() || result.LastSeenAt.IsZero() {
		t.Errorf("first seen = %v last seen = %v, want both recorded", result.FirstSeenAt, result.LastSeenAt)
	}
}

func TestSearchBusinessesReadsSocialTechnologyAndCheckColumns(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)

	defer closeRepository()

	job := resultImportJob("job-core-children", time.Now().UTC())
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatalf("create job: %v", err)
	}

	path := filepath.Join(t.TempDir(), "children.csv")
	writeLegacyResultRows(t, path, map[string]string{
		"title": "Harbor Plumbing", "category": "Plumber", "place_id": "child-1",
		"address": "9 Pier St, San Francisco, CA 94105, United States",
		"website": "https://harbor.example", "phone": "+1 415 555 0111",
	})

	if _, err := repository.ImportLegacyCSV(ctx, job, path); err != nil {
		t.Fatalf("import child rows: %v", err)
	}

	page, err := repository.SearchBusinesses(ctx, web.ResultSearch{Limit: 5})
	if err != nil {
		t.Fatalf("SearchBusinesses() error = %v", err)
	}

	if len(page.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(page.Results))
	}

	businessID := page.Results[0].ID
	checkedAt := time.Now().UTC().Add(-time.Hour).Unix()

	if _, err := repository.db.ExecContext(ctx,
		`INSERT INTO social_profiles(business_id, platform, url, source_url, confidence)
		VALUES (?, 'linkedin', 'https://linkedin.com/company/harbor', '', 0.9),
			(?, 'facebook', 'https://facebook.com/harbor', '', 0.8)`,
		businessID, businessID,
	); err != nil {
		t.Fatalf("insert social profiles: %v", err)
	}

	if _, err := repository.db.ExecContext(ctx,
		`INSERT INTO websites(business_id, url, domain, status, technologies, last_checked_at)
		VALUES (?, 'https://harbor.example', 'harbor.example', 'active', '["WordPress","nginx"]', ?)`,
		businessID, checkedAt,
	); err != nil {
		t.Fatalf("insert website audit: %v", err)
	}

	// The import already stored the listed phone, so the enrichment pass only
	// classifies it rather than inserting a second row.
	if _, err := repository.db.ExecContext(ctx,
		`UPDATE phones SET kind = 'mobile' WHERE business_id = ?`,
		businessID,
	); err != nil {
		t.Fatalf("classify phone: %v", err)
	}

	page, err = repository.SearchBusinesses(ctx, web.ResultSearch{Limit: 5})
	if err != nil {
		t.Fatalf("SearchBusinesses() after enrichment error = %v", err)
	}

	result := page.Results[0]
	if result.Social.LinkedIn != "https://linkedin.com/company/harbor" {
		t.Errorf("linkedin = %q, want the stored profile", result.Social.LinkedIn)
	}

	if result.Social.Facebook == "" || result.Social.Any() != true {
		t.Errorf("social = %+v, want both stored platforms", result.Social)
	}

	if len(result.Technologies) != 2 {
		t.Errorf("technologies = %v, want both detected technologies", result.Technologies)
	}

	if result.LastCheckedAt == nil || result.LastCheckedAt.Unix() != checkedAt {
		t.Errorf("last checked = %v, want %d", result.LastCheckedAt, checkedAt)
	}

	if result.PhoneType != "mobile" {
		t.Errorf("phone type = %q, want mobile", result.PhoneType)
	}
}
