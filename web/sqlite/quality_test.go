package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

func TestCalculateQualityExplainsPositiveNegativeAndClosedExclusion(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	rules := web.DefaultQualityRuleSet()
	input := qualityInput{
		businessStatus: "Open", website: "https://example.com", phone: "+14155550100",
		category: "Dentist", address: "1 Market St", description: "Local dentist",
		latitude: sqlNullFloat(37.78), longitude: sqlNullFloat(-122.4), rating: sqlNullFloat(4.8),
		reviewCount: sqlNullInt(120), placeID: "place-1", lastSeenAt: now.Unix(),
		websiteStatus: "active", websiteHTTPS: sqlNullInt(1), responseTimeMS: sqlNullInt(400),
		pageTitle: "Example", metaDescription: "Example dentist", websiteAudited: 1,
		emailCount: 1, validEmailCount: 1, socialCount: 1,
	}
	components, score, confidence := calculateQuality(input, rules, now)
	if len(components) != 12 || score < 95 || confidence != 1 {
		t.Fatalf("healthy score = %.2f confidence=%.2f components=%+v", score, confidence, components)
	}

	input.businessStatus = "Permanently closed"
	input.websiteStatus = "inactive"
	input.emailCount = 1
	input.validEmailCount = 0
	components, degradedScore, _ := calculateQuality(input, rules, now)
	if degradedScore >= score {
		t.Fatalf("degraded score = %.2f, healthy = %.2f", degradedScore, score)
	}
	foundNegative := false
	for _, component := range components {
		if component.Contribution < 0 {
			foundNegative = true
		}
	}
	if !foundNegative {
		t.Fatalf("expected negative contribution: %+v", components)
	}
	rules.ExcludeClosed = true
	_, excludedScore, _ := calculateQuality(input, rules, now)
	if excludedScore != 0 {
		t.Fatalf("excluded closed score = %.2f, want 0", excludedScore)
	}
}

func TestQualityRulesAreVersionedAndReportsPersistBreakdown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, err := New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	concrete := repository.(*repo)
	t.Cleanup(func() { _ = concrete.db.Close() })

	observedAt := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	job := resultImportJob("quality-job", observedAt)
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "quality.csv")
	writeLegacyResultRows(t, path, map[string]string{
		"title": "Quality Dental", "category": "Dentist", "status": "Open",
		"address": "1 Market St, San Francisco", "website": "https://quality.example",
		"phone": "+1 415 555 0100", "emails": "info@quality.example", "place_id": "quality-place",
		"review_rating": "4.7", "review_count": "80", "latitude": "37.78", "longitude": "-122.4",
	})
	if _, err := concrete.ImportLegacyCSV(ctx, job, path); err != nil {
		t.Fatalf("ImportLegacyCSV() error = %v", err)
	}
	var businessID string
	if err := concrete.db.QueryRowContext(ctx, "SELECT id FROM businesses WHERE place_id = 'quality-place'").Scan(&businessID); err != nil {
		t.Fatalf("read business ID: %v", err)
	}

	initial, err := concrete.BusinessQuality(ctx, businessID)
	if err != nil {
		t.Fatalf("BusinessQuality() error = %v", err)
	}
	if initial.RuleVersion != "builtin-v1" || len(initial.Contributions) != 12 {
		t.Fatalf("initial quality report = %+v", initial)
	}

	rules, err := concrete.ActiveQualityRules(ctx)
	if err != nil {
		t.Fatalf("ActiveQualityRules() error = %v", err)
	}
	rules.Name = "Phone-first prospects"
	rules.PhoneWeight = 30
	saved, err := concrete.SaveQualityRules(ctx, rules)
	if err != nil {
		t.Fatalf("SaveQualityRules() error = %v", err)
	}
	if saved.Version == "" || saved.Version == initial.RuleVersion {
		t.Fatalf("saved quality rules = %+v", saved)
	}
	if count, err := concrete.RecalculateQuality(ctx, []string{businessID}); err != nil || count != 1 {
		t.Fatalf("RecalculateQuality() = %d, %v", count, err)
	}
	recalculated, err := concrete.BusinessQuality(ctx, businessID)
	if err != nil {
		t.Fatalf("BusinessQuality(recalculated) error = %v", err)
	}
	if recalculated.RuleVersion != saved.Version || recalculated.RuleName != saved.Name || len(recalculated.Contributions) != 12 {
		t.Fatalf("recalculated quality report = %+v", recalculated)
	}
	var versions int64
	if err := concrete.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM quality_rule_sets").Scan(&versions); err != nil || versions != 2 {
		t.Fatalf("quality rule versions = %d, %v", versions, err)
	}
}

func sqlNullFloat(value float64) (result sql.NullFloat64) {
	result.Float64 = value
	result.Valid = true
	return result
}

func sqlNullInt(value int64) (result sql.NullInt64) {
	result.Int64 = value
	result.Valid = true
	return result
}
