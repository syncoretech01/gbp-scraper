package web

import (
	"errors"
	"net/http"
	"net/http/httptest"

	"slices"
	"strings"
	"testing"

	"github.com/gosom/google-maps-scraper/web/enrichment"
)

// TestEnrichmentURLPatternsRoundTripFromJobData covers the wiring clause: a
// job's configured crawl URL patterns reach the per-task enrichment options
// canonicalised, and from there the crawler configuration.
func TestEnrichmentURLPatternsRoundTripFromJobData(t *testing.T) {
	t.Parallel()

	options, enabled, err := EnrichmentOptionsForJob(JobData{Enrichment: &JobEnrichmentOptions{
		Website:            true,
		IncludeURLPatterns: []string{" /Contact* ", "/contact*", ""},
		ExcludeURLPatterns: []string{"/BLOG/*"},
	}})
	if err != nil || !enabled {
		t.Fatalf("EnrichmentOptionsForJob() = %+v, %v, %v", options, enabled, err)
	}

	// Normalisation lowercases, trims, drops blanks, and de-duplicates.
	if !slices.Equal(options.IncludeURLPatterns, []string{"/contact*"}) {
		t.Fatalf("include patterns = %v, want the canonicalised single entry", options.IncludeURLPatterns)
	}

	if !slices.Equal(options.ExcludeURLPatterns, []string{"/blog/*"}) {
		t.Fatalf("exclude patterns = %v, want the canonicalised single entry", options.ExcludeURLPatterns)
	}

	config := options.crawlerConfig()
	if !slices.Equal(config.URLPatterns.Include, []string{"/contact*"}) ||
		!slices.Equal(config.URLPatterns.Exclude, []string{"/blog/*"}) {
		t.Fatalf("crawler config patterns = %+v, want the configured set", config.URLPatterns)
	}

	// No patterns must leave the crawler configuration exactly as it was.
	bare, _, err := EnrichmentOptionsForJob(JobData{Enrichment: &JobEnrichmentOptions{Website: true}})
	if err != nil {
		t.Fatalf("EnrichmentOptionsForJob(bare) error = %v", err)
	}

	if !bare.crawlerConfig().URLPatterns.Empty() {
		t.Fatal("a job without patterns must produce an empty, non-filtering pattern set")
	}
}

func TestEnrichmentURLPatternsAreBounded(t *testing.T) {
	t.Parallel()

	tooMany := make([]string, 0, enrichment.MaximumURLPatterns+1)
	for index := range enrichment.MaximumURLPatterns + 1 {
		tooMany = append(tooMany, "/page-"+string(rune('a'+index%26))+strings.Repeat("x", index))
	}

	if err := (JobEnrichmentOptions{Website: true, IncludeURLPatterns: tooMany}).Validate(); err == nil {
		t.Fatal("Validate() accepted more include patterns than the bound allows")
	} else if !errors.Is(err, ErrInvalidEnrichment) {
		t.Fatalf("Validate() error = %v, want ErrInvalidEnrichment", err)
	}

	long := "/" + strings.Repeat("a", enrichment.MaximumURLPatternLength)
	if err := (JobEnrichmentOptions{Website: true, ExcludeURLPatterns: []string{long}}).Validate(); err == nil {
		t.Fatal("Validate() accepted an over-long exclude pattern")
	}
}

// TestPreclassifyClearsURLPatterns documents the preclassify decision: the
// probe fetches only the homepage, so it has nothing for patterns to act on
// and must not record evidence claiming a filter was applied.
func TestPreclassifyClearsURLPatterns(t *testing.T) {
	t.Parallel()

	options, err := (EnrichmentOptions{
		Preclassify:        true,
		IncludeURLPatterns: []string{"/contact*"},
		ExcludeURLPatterns: []string{"/blog/*"},
	}).normalized()
	if err != nil {
		t.Fatalf("normalized() error = %v", err)
	}

	if len(options.IncludeURLPatterns) != 0 || len(options.ExcludeURLPatterns) != 0 {
		t.Fatalf("preclassify kept crawl URL patterns: %+v", options)
	}
}

func TestParseWizardJobReadsEnrichmentURLPatternsAndMemoryCeiling(t *testing.T) {
	t.Parallel()

	form := validWizardForm("")
	// "email" is the wizard's long-standing name for the local website audit.
	form.Set("email", "on")
	form.Set("enrichment_include_url_patterns", "/contact*\n/about")
	form.Set("enrichment_exclude_url_patterns", "/blog/*, */cart*")
	form.Set("memory_ceiling_mb", "4096")

	request := httptest.NewRequest(http.MethodPost, "/app/scrapes", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	job, _, err := parseWizardJob(request)
	if err != nil {
		t.Fatalf("parseWizardJob() error = %v", err)
	}

	if job.Data.Enrichment == nil {
		t.Fatal("the wizard did not build enrichment options")
	}

	if !slices.Equal(job.Data.Enrichment.IncludeURLPatterns, []string{"/contact*", "/about"}) {
		t.Fatalf("include patterns = %v", job.Data.Enrichment.IncludeURLPatterns)
	}

	if !slices.Equal(job.Data.Enrichment.ExcludeURLPatterns, []string{"/blog/*", "*/cart*"}) {
		t.Fatalf("exclude patterns = %v", job.Data.Enrichment.ExcludeURLPatterns)
	}

	const wantCeiling = 4096 << 20
	if job.Data.MemoryCeilingBytes != wantCeiling {
		t.Fatalf("memory ceiling = %d bytes, want %d", job.Data.MemoryCeilingBytes, wantCeiling)
	}
}

// TestParseWizardJobWithoutMemoryCeilingKeepsTodaysBehaviour pins the
// compatibility clause: an empty field means no ceiling.
func TestParseWizardJobWithoutMemoryCeilingKeepsTodaysBehaviour(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(
		http.MethodPost, "/app/scrapes", strings.NewReader(validWizardForm("").Encode()),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	job, _, err := parseWizardJob(request)
	if err != nil {
		t.Fatalf("parseWizardJob() error = %v", err)
	}

	if job.Data.MemoryCeilingBytes != 0 {
		t.Fatalf("memory ceiling = %d, want 0 for an unset field", job.Data.MemoryCeilingBytes)
	}

	if err := job.Data.Validate(); err != nil {
		t.Fatalf("a job without a ceiling must validate: %v", err)
	}
}

func TestJobDataValidatesTheMemoryCeilingBounds(t *testing.T) {
	t.Parallel()

	valid := validJobData()
	valid.MemoryCeilingBytes = MinimumMemoryCeilingBytes

	if err := valid.Validate(); err != nil {
		t.Fatalf("the minimum ceiling must validate: %v", err)
	}

	tooSmall := validJobData()
	tooSmall.MemoryCeilingBytes = MinimumMemoryCeilingBytes - 1

	if err := tooSmall.Validate(); err == nil {
		t.Fatal("a ceiling below the minimum must be refused")
	}

	tooLarge := validJobData()
	tooLarge.MemoryCeilingBytes = MaximumMemoryCeilingBytes + 1

	if err := tooLarge.Validate(); err == nil {
		t.Fatal("a ceiling above the maximum must be refused")
	}
}
