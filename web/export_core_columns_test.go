package web

import (
	"testing"
	"time"
)

// A core column that renders but cannot leave the workspace is only half a
// column. This test pins the export side: every specification core field has an
// export column, and each one reads the value the table shows.

func TestExportColumnsCoverEverySpecificationCoreField(t *testing.T) {
	t.Parallel()

	available := make(map[string]bool, len(exportColumnDefinitions))
	for _, definition := range exportColumnDefinitions {
		available[definition.Key] = true
	}

	for _, key := range []string{
		"name", "category", "additional_categories", "description", "claimed", "business_status",
		"address", "street", "city", "state", "postal_code", "country",
		"latitude", "longitude", "plus_code",
		"phone", "normalized_phone", "phone_type", "website", "domain",
		"email", "emails", "email_type", "email_status",
		"facebook", "instagram", "linkedin", "x", "youtube", "tiktok", "whatsapp",
		"rating", "review_count", "reviews_per_rating", "user_reviews", "popular_times",
		"place_id", "cid", "data_id", "maps_url", "source_query", "source_cell", "input_id",
		"website_status", "website_response_ms", "technologies", "quality_score", "confidence",
		"last_checked_at", "tags", "notes", "reviewed", "scraped_at", "updated_at", "change_status",
	} {
		if !available[key] {
			t.Errorf("export column %q is missing", key)
		}
	}
}

func TestExportColumnValueReadsTheCoreColumns(t *testing.T) {
	t.Parallel()

	row := exportDataRow{Business: coreColumnResultRow()}

	tests := map[string]any{
		"description":  "Family dentistry in SoMa.",
		"street":       "123 Market St",
		"plus_code":    "849VQH48+92",
		"input_id":     "seed-42",
		"phone_type":   "landline",
		"emails":       "info@example.test; hi@example.test",
		"email_type":   "role",
		"email_status": "unverified",
		"linkedin":     "https://linkedin.com/company/baysmile",
		"facebook":     "https://facebook.com/baysmile",
		"technologies": "WordPress; nginx",
		"user_reviews": `[{"Name":"A"},{"Name":"B"},{"Name":"C"}]`,
	}

	for key, want := range tests {
		got, err := exportColumnValue(row, key)
		if err != nil {
			t.Fatalf("exportColumnValue(%q) error = %v", key, err)
		}

		if got != want {
			t.Errorf("exportColumnValue(%q) = %v, want %v", key, got, want)
		}
	}

	checked, err := exportColumnValue(row, "last_checked_at")
	if err != nil {
		t.Fatalf("exportColumnValue(last_checked_at) error = %v", err)
	}

	if checked == nil {
		t.Fatal("last checked exported as nil, want the stored timestamp")
	}

	// An unchecked website exports no timestamp rather than the zero time.
	unchecked := row
	unchecked.Business.LastCheckedAt = nil

	empty, err := exportColumnValue(unchecked, "last_checked_at")
	if err != nil {
		t.Fatalf("exportColumnValue(last_checked_at) unchecked error = %v", err)
	}

	if empty != nil {
		t.Fatalf("unchecked website exported %v, want nil", empty)
	}

	seen, err := exportColumnValue(row, "first_seen_at")
	if err != nil {
		t.Fatalf("exportColumnValue(first_seen_at) error = %v", err)
	}

	if seen != row.Business.FirstSeenAt.UTC().Format(time.RFC3339) {
		t.Fatalf("first seen exported %v, want RFC3339", seen)
	}
}
