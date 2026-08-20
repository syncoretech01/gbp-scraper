package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web/enrichment"
)

type historyReadback struct {
	businessID string
	websiteURL string
	limit      int
}

type workerEvent struct {
	action   string
	entityID string
	details  string
}

// adaptiveRepositoryStub extends the shared enrichment stub with the optional
// history readback so the worker can take its adaptive path.
type adaptiveRepositoryStub struct {
	*enrichmentRepositoryStub
	history       enrichment.SiteHistory
	historyErr    error
	readbacks     []historyReadback
	workerEvents  []workerEvent
	storedOptions EnrichmentOptions
}

func (repository *adaptiveRepositoryStub) WebsiteLatencyHistory(
	_ context.Context,
	businessID string,
	websiteURL string,
	limit int,
) (enrichment.SiteHistory, error) {
	repository.readbacks = append(repository.readbacks, historyReadback{
		businessID: businessID, websiteURL: websiteURL, limit: limit,
	})
	if repository.historyErr != nil {
		return enrichment.SiteHistory{}, repository.historyErr
	}

	return repository.history, nil
}

func (repository *adaptiveRepositoryStub) RecordEnrichmentEvent(
	_ context.Context,
	action string,
	entityID string,
	details string,
) error {
	repository.workerEvents = append(repository.workerEvents, workerEvent{
		action: action, entityID: entityID, details: details,
	})

	return nil
}

func (repository *adaptiveRepositoryStub) StoreWebsiteAudit(
	ctx context.Context,
	task EnrichmentTask,
	analysis enrichment.Result,
	startedAt time.Time,
	completedAt time.Time,
) (int64, error) {
	repository.storedOptions = task.Options

	return repository.enrichmentRepositoryStub.StoreWebsiteAudit(ctx, task, analysis, startedAt, completedAt)
}

func adaptiveTask(adaptive, preclassify bool) EnrichmentTask {
	options := EnrichmentOptions{
		Scope: string(enrichment.ScopeHomepage), MaxPages: 1, TimeoutSeconds: 10,
		MaxBodyBytes: 2048, MaxRedirects: 2, DisableInternalChecks: true,
		AdaptiveTimeout: adaptive, Preclassify: preclassify,
	}

	return EnrichmentTask{
		ID: "task-adaptive", BusinessID: "business-adaptive",
		WebsiteURL: "https://adaptive.example", State: "queued", Options: options,
	}
}

func fastHealthyHistory() enrichment.SiteHistory {
	return enrichment.SiteHistory{
		Host:       "adaptive.example",
		LastStatus: "active",
		Observations: []enrichment.SiteObservation{
			{ResponseTime: 400 * time.Millisecond, Reachable: true},
			{ResponseTime: 380 * time.Millisecond, Reachable: true},
			{ResponseTime: 420 * time.Millisecond, Reachable: true},
		},
	}
}

// runAdaptiveWorker processes one task and returns the timeout the analyzer
// was actually built with.
func runAdaptiveWorker(t *testing.T, repository *adaptiveRepositoryStub) time.Duration {
	t.Helper()

	var observed time.Duration
	factory := func(received EnrichmentOptions) (websiteAnalyzer, error) {
		observed = received.crawlerConfig().Timeout

		return analyzerStub{result: enrichment.Result{
			RequestedURL: "https://adaptive.example", FinalURL: "https://adaptive.example",
			Reachable: true, StatusCode: 200,
		}}, nil
	}

	service := NewService(repository, t.TempDir())
	processed, err := service.processEnrichmentQueue(context.Background(), 1, factory)
	if err != nil || processed != 1 {
		t.Fatalf("processEnrichmentQueue() = %d, %v", processed, err)
	}

	return observed
}

func TestAdaptiveTimeoutIsOffByDefaultAndKeepsTheConfiguredBudget(t *testing.T) {
	t.Parallel()

	task := adaptiveTask(false, false)
	repository := &adaptiveRepositoryStub{
		enrichmentRepositoryStub: &enrichmentRepositoryStub{next: &task},
		history:                  fastHealthyHistory(),
	}

	if observed := runAdaptiveWorker(t, repository); observed != 10*time.Second {
		t.Fatalf("analyzer timeout = %v, want the configured 10s", observed)
	}
	if len(repository.readbacks) != 0 {
		t.Fatalf("history readbacks = %+v, want none when the option is absent", repository.readbacks)
	}
	if len(repository.workerEvents) != 0 {
		t.Fatalf("worker events = %+v, want none when the option is absent", repository.workerEvents)
	}
}

func TestAdaptiveTimeoutShortensTheAnalyzerBudgetWhenEnabled(t *testing.T) {
	t.Parallel()

	task := adaptiveTask(true, false)
	repository := &adaptiveRepositoryStub{
		enrichmentRepositoryStub: &enrichmentRepositoryStub{next: &task},
		history:                  fastHealthyHistory(),
	}

	observed := runAdaptiveWorker(t, repository)
	want := enrichment.AdaptiveTimeout(10*time.Second, fastHealthyHistory())
	if observed != want {
		t.Fatalf("analyzer timeout = %v, want the policy budget %v", observed, want)
	}
	if observed >= 10*time.Second {
		t.Fatalf("analyzer timeout = %v, want less than the configured ceiling", observed)
	}

	if len(repository.readbacks) != 1 {
		t.Fatalf("history readbacks = %+v, want exactly one per claimed task", repository.readbacks)
	}
	readback := repository.readbacks[0]
	if readback.businessID != task.BusinessID || readback.websiteURL != task.WebsiteURL ||
		readback.limit != adaptiveTimeoutHistoryWindow {
		t.Fatalf("history readback = %+v", readback)
	}

	if len(repository.workerEvents) != 1 {
		t.Fatalf("worker events = %+v, want one adaptation record", repository.workerEvents)
	}
	event := repository.workerEvents[0]
	if event.action != adaptiveTimeoutEventAction || event.entityID != task.ID {
		t.Fatalf("worker event = %+v", event)
	}
	var evidence adaptiveTimeoutEvidence
	if err := json.Unmarshal([]byte(event.details), &evidence); err != nil {
		t.Fatalf("decode event details %q: %v", event.details, err)
	}
	if evidence.ConfiguredMS != 10000 || evidence.AdaptedMS != want.Milliseconds() ||
		evidence.Host != "adaptive.example" || evidence.Observations != 3 {
		t.Fatalf("adaptation evidence = %+v", evidence)
	}

	// The persisted task options must still record what the operator asked
	// for. The resolved budget is runtime state and never serializes.
	if repository.storedOptions.TimeoutSeconds != 10 {
		t.Fatalf("stored options = %+v, want the configured timeout preserved", repository.storedOptions)
	}
	encoded, err := json.Marshal(EnrichmentOptions{TimeoutSeconds: 10, resolvedTimeout: time.Second})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), "resolved") || strings.Contains(string(encoded), "1000") {
		t.Fatalf("encoded options = %s, want no runtime timeout leakage", encoded)
	}
}

func TestAdaptiveTimeoutHonorsThePreclassifyProfileCeiling(t *testing.T) {
	t.Parallel()

	options, err := (EnrichmentOptions{Preclassify: true, AdaptiveTimeout: true}).normalized()
	if err != nil {
		t.Fatalf("normalized() error = %v", err)
	}
	if !options.AdaptiveTimeout || options.TimeoutSeconds != preclassifyDefaultTimeoutSeconds {
		t.Fatalf("preclassify profile = %+v", options)
	}

	task := adaptiveTask(true, true)
	task.Options = options
	repository := &adaptiveRepositoryStub{
		enrichmentRepositoryStub: &enrichmentRepositoryStub{next: &task},
		history:                  fastHealthyHistory(),
	}

	ceiling := time.Duration(preclassifyDefaultTimeoutSeconds) * time.Second
	want := enrichment.AdaptiveTimeout(ceiling, fastHealthyHistory())
	observed := runAdaptiveWorker(t, repository)
	if observed != want || observed >= ceiling {
		t.Fatalf("preclassify analyzer timeout = %v, want the policy budget %v below %v", observed, want, ceiling)
	}
	if len(repository.workerEvents) != 1 || !strings.Contains(repository.workerEvents[0].details, `"preclassify":true`) {
		t.Fatalf("preclassify adaptation events = %+v", repository.workerEvents)
	}
}

func TestAdaptiveTimeoutFallsBackToTheConfiguredBudget(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		history    enrichment.SiteHistory
		historyErr error
	}{
		{name: "no observed history", history: enrichment.SiteHistory{}},
		{name: "history readback failure", historyErr: errors.New("database is locked")},
		{
			name: "history that does not shrink the budget",
			history: enrichment.SiteHistory{Observations: []enrichment.SiteObservation{
				{ResponseTime: 9 * time.Second, Reachable: true},
				{ResponseTime: 9 * time.Second, Reachable: true},
			}},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			task := adaptiveTask(true, false)
			repository := &adaptiveRepositoryStub{
				enrichmentRepositoryStub: &enrichmentRepositoryStub{next: &task},
				history:                  testCase.history,
				historyErr:               testCase.historyErr,
			}

			if observed := runAdaptiveWorker(t, repository); observed != 10*time.Second {
				t.Fatalf("analyzer timeout = %v, want the configured 10s", observed)
			}
			if len(repository.workerEvents) != 0 {
				t.Fatalf("worker events = %+v, want none without an adaptation", repository.workerEvents)
			}
		})
	}
}

func TestAdaptiveTimeoutIgnoredByRepositoriesWithoutHistory(t *testing.T) {
	t.Parallel()

	task := adaptiveTask(true, false)
	repository := &enrichmentRepositoryStub{next: &task}

	var observed time.Duration
	factory := func(received EnrichmentOptions) (websiteAnalyzer, error) {
		observed = received.crawlerConfig().Timeout

		return analyzerStub{result: enrichment.Result{
			RequestedURL: task.WebsiteURL, FinalURL: task.WebsiteURL, Reachable: true, StatusCode: 200,
		}}, nil
	}
	service := NewService(repository, t.TempDir())
	processed, err := service.processEnrichmentQueue(context.Background(), 1, factory)
	if err != nil || processed != 1 {
		t.Fatalf("processEnrichmentQueue() = %d, %v", processed, err)
	}
	if observed != 10*time.Second {
		t.Fatalf("analyzer timeout = %v, want the configured 10s", observed)
	}
}

func TestAdaptiveTimeoutOptionDecodesFromJSONAndForm(t *testing.T) {
	t.Parallel()

	var options EnrichmentOptions
	if err := json.Unmarshal([]byte(`{"adaptive_timeout":true}`), &options); err != nil ||
		!options.AdaptiveTimeout {
		t.Fatalf("Unmarshal() = %+v, %v", options, err)
	}

	encoded, err := json.Marshal(EnrichmentOptions{})
	if err != nil || strings.Contains(string(encoded), "adaptive_timeout") {
		t.Fatalf("absent option encoding = %s, %v", encoded, err)
	}

	jobOptions, enabled, err := EnrichmentOptionsForJob(JobData{Enrichment: &JobEnrichmentOptions{
		Website: true, AdaptiveTimeout: true,
	}})
	if err != nil || !enabled || !jobOptions.AdaptiveTimeout {
		t.Fatalf("EnrichmentOptionsForJob() = %+v, %v, %v", jobOptions, enabled, err)
	}
	if err := (JobEnrichmentOptions{AdaptiveTimeout: true}).Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	defaults, _, err := EnrichmentOptionsForJob(JobData{Enrichment: &JobEnrichmentOptions{Website: true}})
	if err != nil || defaults.AdaptiveTimeout {
		t.Fatalf("default job options = %+v, %v", defaults, err)
	}

	// POST /api/v1/results/enrich decodes strictly, so the additive option has
	// to be a known field of the request payload.
	jsonRequest := httptest.NewRequest(
		"POST",
		"/api/v1/results/enrich",
		strings.NewReader(`{"ids":["business-1"],"options":{"adaptive_timeout":true,"timeout_seconds":8}}`),
	)
	jsonRequest.Header.Set("Content-Type", "application/json")
	decoded, err := decodeEnrichmentRequest(httptest.NewRecorder(), jsonRequest)
	if err != nil || !decoded.Options.AdaptiveTimeout || decoded.Options.TimeoutSeconds != 8 {
		t.Fatalf("decodeEnrichmentRequest(JSON) = %+v, %v", decoded, err)
	}

	formRequest := httptest.NewRequest(
		"POST",
		"/api/v1/results/enrich",
		strings.NewReader("result_ids=business-1&enrichment_adaptive_timeout=on"),
	)
	formRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	fromForm, err := decodeEnrichmentRequest(httptest.NewRecorder(), formRequest)
	if err != nil || !fromForm.Options.AdaptiveTimeout {
		t.Fatalf("decodeEnrichmentRequest(form) = %+v, %v", fromForm, err)
	}

	absentForm := httptest.NewRequest(
		"POST", "/api/v1/results/enrich", strings.NewReader("result_ids=business-1"),
	)
	absentForm.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	withoutOption, err := decodeEnrichmentRequest(httptest.NewRecorder(), absentForm)
	if err != nil || withoutOption.Options.AdaptiveTimeout {
		t.Fatalf("decodeEnrichmentRequest(absent) = %+v, %v", withoutOption, err)
	}
}
