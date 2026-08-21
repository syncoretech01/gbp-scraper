package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

// templateTestRepository is a JobRepository that also stores scrape
// templates, which is everything the campaign-template service needs.
type templateTestRepository struct {
	*campaignTestRepository

	templates map[string]ScrapeTemplate
	uses      map[string]int64
}

func newTemplateTestRepository(seed ...Job) *templateTestRepository {
	return &templateTestRepository{
		campaignTestRepository: newCampaignTestRepository(seed...),
		templates:              make(map[string]ScrapeTemplate),
		uses:                   make(map[string]int64),
	}
}

func (r *templateTestRepository) ListScrapeTemplates(
	context.Context,
	string,
) ([]ScrapeTemplate, error) {
	templates := make([]ScrapeTemplate, 0, len(r.templates))
	for _, template := range r.templates {
		templates = append(templates, template)
	}

	return templates, nil
}

func (r *templateTestRepository) GetScrapeTemplate(
	_ context.Context,
	id string,
) (ScrapeTemplate, error) {
	template, ok := r.templates[id]
	if !ok {
		return ScrapeTemplate{}, ErrReusableNotFound
	}

	return template, nil
}

func (r *templateTestRepository) SaveScrapeTemplate(_ context.Context, template ScrapeTemplate) error {
	r.templates[template.ID] = template

	return nil
}

func (r *templateTestRepository) DeleteScrapeTemplate(_ context.Context, id string) error {
	delete(r.templates, id)

	return nil
}

func (r *templateTestRepository) SetScrapeTemplatePinned(context.Context, string, bool) error {
	return nil
}

func (r *templateTestRepository) RecordScrapeTemplateUse(
	_ context.Context,
	id string,
	_ time.Time,
) error {
	r.uses[id]++

	return nil
}

func (r *templateTestRepository) ListSavedResultViews(
	context.Context,
	string,
) ([]SavedResultView, error) {
	return nil, nil
}

func (r *templateTestRepository) GetSavedResultView(
	context.Context,
	string,
) (SavedResultView, error) {
	return SavedResultView{}, ErrReusableNotFound
}

func (r *templateTestRepository) SaveResultView(context.Context, SavedResultView) error { return nil }

func (r *templateTestRepository) DeleteSavedResultView(context.Context, string) error { return nil }

func campaignTemplateJob() Job {
	return Job{
		ID:     "5b7f2e10-8a41-4bd1-9d3a-2c1b4e6f7a80",
		Name:   "Denver roofers",
		Date:   time.Unix(1_700_000_000, 0).UTC(),
		Status: StatusOK,
		Data: JobData{
			Keywords: []string{
				"roofer in Denver CO 80202",
				"roofing contractor in Denver CO 80202",
				"roofer in Denver CO 80203",
			},
			Lang: "en", Zoom: 14, Depth: 12, MaxTime: 45 * time.Minute,
			Email:      true,
			Enrichment: &JobEnrichmentOptions{Website: true, Emails: true, MaxPages: 3, TimeoutSeconds: 10},
			Coverage:   &CoverageOptions{AutoStop: true, MaxExpansions: 20, ExpansionMinNew: 6},
			Proxies:    []string{"http://user:secret@proxy.example:8080"},
		},
	}
}

func TestCampaignTemplateShapeSummarisesTheCampaign(t *testing.T) {
	t.Parallel()

	shape := CampaignTemplateShapeOf(campaignTemplateJob().Data)

	if shape.Queries != 3 || shape.ZIPQueries != 3 || shape.ZIPs != 2 {
		t.Fatalf("shape = %#v, want 3 queries over 2 ZIP cells", shape)
	}

	if !shape.Coverage || !shape.Enrichment {
		t.Fatalf("shape = %#v, want coverage and enrichment recorded", shape)
	}

	plain := CampaignTemplateShapeOf(JobData{Keywords: []string{"dentist"}})
	if plain.ZIPQueries != 0 || plain.ZIPs != 0 || plain.Coverage || plain.Enrichment {
		t.Fatalf("plain shape = %#v", plain)
	}
}

func TestCaptureCampaignTemplateKeepsTheWholeShapeAndDropsProxies(t *testing.T) {
	t.Parallel()

	job := campaignTemplateJob()
	repository := newTemplateTestRepository(job)
	service := NewService(repository, t.TempDir())

	result, err := service.CaptureCampaignTemplate(
		context.Background(), job.ID, "", "Captured from the Denver campaign",
	)
	if err != nil {
		t.Fatalf("CaptureCampaignTemplate: %v", err)
	}

	if result.Name != job.Name || result.Shape.ZIPs != 2 {
		t.Fatalf("capture result = %#v", result)
	}

	template, err := service.GetScrapeTemplate(context.Background(), result.TemplateID)
	if err != nil {
		t.Fatalf("read captured template: %v", err)
	}

	if len(template.Configuration.Keywords) != 3 || template.Configuration.Coverage == nil ||
		template.Configuration.Coverage.MaxExpansions != 20 || template.Configuration.Enrichment == nil {
		t.Fatalf("captured configuration = %#v", template.Configuration)
	}

	// A template is an exportable document, so it may never carry inline
	// proxy credentials.
	if len(template.Configuration.Proxies) != 0 {
		t.Fatalf("captured template carried proxies: %#v", template.Configuration.Proxies)
	}
}

func TestInstantiateCampaignTemplateCreatesARunnableJob(t *testing.T) {
	t.Parallel()

	job := campaignTemplateJob()
	repository := newTemplateTestRepository(job)
	service := NewService(repository, t.TempDir())

	captured, err := service.CaptureCampaignTemplate(context.Background(), job.ID, "Denver roofers", "")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}

	result, err := service.InstantiateCampaignTemplate(
		context.Background(), captured.TemplateID, "Denver roofers August", RerunModeNewOnly, false,
	)
	if err != nil {
		t.Fatalf("InstantiateCampaignTemplate: %v", err)
	}

	if result.JobID == "" || result.State != string(jobruntime.StateQueued) {
		t.Fatalf("instantiation = %#v", result)
	}

	created, err := service.Get(context.Background(), result.JobID)
	if err != nil {
		t.Fatalf("read instantiated job: %v", err)
	}

	if created.Name != "Denver roofers August" || len(created.Data.Keywords) != 3 ||
		created.Data.Coverage == nil || created.Data.IncrementalMode != IncrementalModeNewOnly {
		t.Fatalf("instantiated job = %#v", created.Data)
	}

	if repository.uses[captured.TemplateID] != 1 {
		t.Fatalf("template use count = %d, want 1", repository.uses[captured.TemplateID])
	}

	// A second instantiation is a second run, not a mutation of the first.
	second, err := service.InstantiateCampaignTemplate(
		context.Background(), captured.TemplateID, "", "", true,
	)
	if err != nil {
		t.Fatalf("second instantiation: %v", err)
	}

	if second.JobID == result.JobID || second.State != string(jobruntime.StateDraft) {
		t.Fatalf("second instantiation = %#v", second)
	}
}

func TestCampaignTemplateRejectsBadInput(t *testing.T) {
	t.Parallel()

	job := campaignTemplateJob()
	service := NewService(newTemplateTestRepository(job), t.TempDir())

	if _, err := service.CaptureCampaignTemplate(
		context.Background(), job.ID, strings.Repeat("x", 200), "",
	); !errors.Is(err, ErrInvalidCampaignTemplate) {
		t.Fatalf("oversized name error = %v, want ErrInvalidCampaignTemplate", err)
	}

	if _, err := service.CaptureCampaignTemplate(
		context.Background(), job.ID, "ok", strings.Repeat("d", 600),
	); !errors.Is(err, ErrInvalidCampaignTemplate) {
		t.Fatalf("oversized description error = %v, want ErrInvalidCampaignTemplate", err)
	}

	captured, err := service.CaptureCampaignTemplate(context.Background(), job.ID, "ok", "")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}

	if _, err := service.InstantiateCampaignTemplate(
		context.Background(), captured.TemplateID, "", "sideways", false,
	); !errors.Is(err, ErrInvalidCampaignTemplate) {
		t.Fatalf("unknown mode error = %v, want ErrInvalidCampaignTemplate", err)
	}

	if _, err := service.InstantiateCampaignTemplate(
		context.Background(), "no-such-template", "", "", false,
	); !errors.Is(err, ErrReusableNotFound) {
		t.Fatalf("missing template error = %v, want ErrReusableNotFound", err)
	}
}

func TestCampaignTemplateAPIRoundTrip(t *testing.T) {
	t.Parallel()

	job := campaignTemplateJob()
	repository := newTemplateTestRepository(job)

	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	capture := func(token string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+job.ID+"/template",
			strings.NewReader(`{"name":"Denver roofers","description":"captured"}`))
		request.Header.Set("Content-Type", "application/json")
		if token != "" {
			request.Header.Set("X-CSRF-Token", token)
		}
		recorder := httptest.NewRecorder()
		server.srv.Handler.ServeHTTP(recorder, request)

		return recorder
	}

	if recorder := capture(""); recorder.Code != http.StatusForbidden {
		t.Fatalf("capture without a CSRF token = %d, want 403", recorder.Code)
	}

	recorder := capture(server.csrfToken)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("capture = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	var captured struct {
		Data CampaignTemplateResult `json:"data"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &captured); err != nil {
		t.Fatalf("decode capture response: %v", err)
	}

	if captured.Data.TemplateID == "" || captured.Data.Shape.ZIPs != 2 {
		t.Fatalf("capture response = %#v", captured.Data)
	}

	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/templates/"+captured.Data.TemplateID+"/instantiate",
		strings.NewReader(`{"name":"Denver roofers rescan","mode":"changed-only"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	recorder = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("instantiate = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	var instantiated struct {
		Data CampaignTemplateResult `json:"data"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &instantiated); err != nil {
		t.Fatalf("decode instantiate response: %v", err)
	}

	if instantiated.Data.JobID == "" || instantiated.Data.State != string(jobruntime.StateQueued) {
		t.Fatalf("instantiate response = %#v", instantiated.Data)
	}

	created, err := server.svc.Get(context.Background(), instantiated.Data.JobID)
	if err != nil || created.Data.IncrementalMode != IncrementalModeNewChanged {
		t.Fatalf("instantiated job = %#v (%v)", created.Data, err)
	}
}
