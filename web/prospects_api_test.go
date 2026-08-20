package web

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gosom/google-maps-scraper/web/prospect"
)

// prospectStubRepository implements the minimal JobRepository plus the
// settings, prospect, and result capabilities the prospect routes exercise.
type prospectStubRepository struct {
	fixedJobRepository
	settings      map[string]string
	businesses    []BusinessResult
	summary       ProspectSummary
	recomputedIDs []string
	weightsSeen   prospect.ScoreWeights
	lastSearch    ResultSearch
}

func newProspectStubRepository() *prospectStubRepository {
	return &prospectStubRepository{settings: map[string]string{}}
}

func (r *prospectStubRepository) LoadSettings(context.Context) (map[string]string, error) {
	values := make(map[string]string, len(r.settings))
	for key, value := range r.settings {
		values[key] = value
	}
	return values, nil
}

func (r *prospectStubRepository) SaveSettings(_ context.Context, values map[string]string) error {
	for key, value := range values {
		r.settings[key] = value
	}
	return nil
}

func (r *prospectStubRepository) RecomputeProspects(_ context.Context, weights prospect.ScoreWeights, ids []string) (int64, error) {
	r.weightsSeen = weights
	r.recomputedIDs = ids
	if len(ids) == 0 {
		return 7, nil
	}
	return int64(len(ids)), nil
}

func (r *prospectStubRepository) ProspectSummary(context.Context) (ProspectSummary, error) {
	return r.summary, nil
}

func (r *prospectStubRepository) ImportLegacyCSV(context.Context, Job, string) (ResultFileImport, error) {
	return ResultFileImport{}, nil
}

func (r *prospectStubRepository) SearchBusinesses(_ context.Context, search ResultSearch) (ResultPage, error) {
	r.lastSearch = search
	total := int64(len(r.businesses))
	if search.Offset >= len(r.businesses) {
		return ResultPage{Total: total, Limit: search.Limit, Offset: search.Offset}, nil
	}
	end := search.Offset + search.Limit
	if end > len(r.businesses) {
		end = len(r.businesses)
	}
	return ResultPage{
		Results: r.businesses[search.Offset:end],
		Total:   total, Limit: search.Limit, Offset: search.Offset,
	}, nil
}

func (r *prospectStubRepository) GetBusiness(context.Context, string) (BusinessDetail, error) {
	return BusinessDetail{}, ErrBusinessNotFound
}

func (r *prospectStubRepository) ResultOverview(context.Context) (ResultOverview, error) {
	return ResultOverview{}, nil
}

func newProspectTestServer(t *testing.T, repository JobRepository) (*Server, *http.ServeMux) {
	t.Helper()
	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	mux := http.NewServeMux()
	server.registerProspectRoutes(mux)
	return server, mux
}

func prospectTestBusinesses() []BusinessResult {
	rating := 4.8
	reviews := int64(120)
	return []BusinessResult{
		{
			ID: "biz-1", Name: "Harbor Dental", PrimaryCategory: "Dentist",
			City: "San Francisco", State: "CA", Country: "US",
			Phone: "+1 415 555 0100", PrimaryEmail: "hello@harbordental.com",
			Website: "https://www.harbordental.com/home", WebsiteStatus: "active",
			MapsURL: "https://maps.google.com/?cid=101", PlaceID: "place-1",
			Rating: &rating, ReviewCount: &reviews, SourceJobID: "job-1",
		},
		{
			ID: "biz-2", Name: "No Web Plumbing", PrimaryCategory: "Plumber",
			City: "Oakland", State: "CA", Country: "US",
			MapsURL: "https://maps.google.com/?cid=102", SourceJobID: "job-1",
		},
	}
}

func TestProspectSummaryEndpointReportsStoredAggregates(t *testing.T) {
	t.Parallel()

	repository := newProspectStubRepository()
	repository.summary = ProspectSummary{
		ByStatus: []DashboardCountPoint{{Label: prospect.StatusNoWebsite, Value: 3}},
		ByTier:   []DashboardCountPoint{{Label: "A", Value: 2}},
		Scored:   5,
	}
	_, mux := newProspectTestServer(t, repository)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/prospects/summary", http.NoBody))
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(body, `"scored":5`) || !strings.Contains(body, prospect.StatusNoWebsite) {
		t.Fatalf("summary = %d, body = %s", recorder.Code, body)
	}

	// A repository without prospect storage reports the capability honestly.
	_, bareMux := newProspectTestServer(t, &fixedJobRepository{})
	recorder = httptest.NewRecorder()
	bareMux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/prospects/summary", http.NoBody))
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("summary without support = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestProspectRecomputeIsCSRFGatedAndCapabilityAware(t *testing.T) {
	t.Parallel()

	repository := newProspectStubRepository()
	server, mux := newProspectTestServer(t, repository)

	// A mutation without the CSRF token is refused before repository work.
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/prospects/recompute", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("recompute without CSRF = %d", recorder.Code)
	}

	// A repository without prospect storage answers 501.
	bareServer, bareMux := newProspectTestServer(t, &fixedJobRepository{})
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/prospects/recompute", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", bareServer.csrfToken)
	bareMux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("recompute without support = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	// JSON selection returns the processed count and the stored weights.
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/prospects/recompute", strings.NewReader(`{"ids":["a","b"]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"processed":2`) {
		t.Fatalf("recompute = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repository.weightsSeen != prospect.DefaultScoreWeights() {
		t.Fatalf("recompute weights = %#v, want defaults", repository.weightsSeen)
	}

	// The HTML form fallback accepts one ID per line and the form token.
	form := url.Values{"ids": {"a\nb\nc"}, "csrf_token": {server.csrfToken}}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/prospects/recompute", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"processed":3`) {
		t.Fatalf("form recompute = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if len(repository.recomputedIDs) != 3 || repository.recomputedIDs[2] != "c" {
		t.Fatalf("form recompute ids = %#v", repository.recomputedIDs)
	}
}

func TestProspectScoringRoundtripValidatesWeights(t *testing.T) {
	t.Parallel()

	server, mux := newProspectTestServer(t, newProspectStubRepository())

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/prospects/scoring", http.NoBody))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"no_website":30`) {
		t.Fatalf("default scoring = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/v1/prospects/scoring", strings.NewReader(`{"no_website":42}`))
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("scoring PUT without CSRF = %d", recorder.Code)
	}

	// A partial JSON body overlays the defaults and round-trips.
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPut, "/api/v1/prospects/scoring", strings.NewReader(`{"no_website":42}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("scoring PUT = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/prospects/scoring", http.NoBody))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"no_website":42`) {
		t.Fatalf("scoring roundtrip = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	for name, payload := range map[string]string{
		"negative weight": `{"no_website":-5}`,
		"unknown field":   `{"bogus":1}`,
	} {
		recorder = httptest.NewRecorder()
		request = httptest.NewRequest(http.MethodPut, "/api/v1/prospects/scoring", strings.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", server.csrfToken)
		mux.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnprocessableEntity {
			t.Fatalf("scoring PUT with %s = %d, body = %s", name, recorder.Code, recorder.Body.String())
		}
	}
}

func TestProspectOpenerTemplatesAreValidated(t *testing.T) {
	t.Parallel()

	server, mux := newProspectTestServer(t, newProspectStubRepository())

	put := func(payload string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPut, "/api/v1/prospects/openers", strings.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", server.csrfToken)
		mux.ServeHTTP(recorder, request)
		return recorder
	}

	if recorder := put(`{"greeting":"Hello there, this template has an unknown key"}`); recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown status key = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder := put(`{"default":"too short"}`); recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("short template = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if recorder := put(`{"` + prospect.StatusNoWebsite + `":"Hi {name}, no default template accompanies this one"}`); recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing default = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	valid := `{"default":"Hi {name}, quick question about your website.","` +
		prospect.StatusNoWebsite + `":"Hi {name}, I could not find a website for you in {city}."}`
	if recorder := put(valid); recorder.Code != http.StatusOK {
		t.Fatalf("valid templates = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/prospects/openers", http.NoBody))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "quick question about your website") {
		t.Fatalf("openers roundtrip = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestProspectIntegrationsRequireLocalBoundaries(t *testing.T) {
	t.Parallel()

	server, mux := newProspectTestServer(t, newProspectStubRepository())

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/v1/prospects/integrations", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("integrations PUT without CSRF = %d", recorder.Code)
	}

	put := func(payload string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPut, "/api/v1/prospects/integrations", strings.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", server.csrfToken)
		mux.ServeHTTP(recorder, request)
		return recorder
	}

	// Public hosts never qualify as a local design boundary.
	if recorder := put(`{"email_verifier_url":"http://example.com/verify","audit_service_url":""}`); recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("public URL = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	valid := `{"email_verifier_url":"http://127.0.0.1:8085/verify","audit_service_url":"http://localhost:9200/audit"}`
	if recorder := put(valid); recorder.Code != http.StatusOK {
		t.Fatalf("loopback URLs = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/prospects/integrations", http.NoBody))
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK ||
		!strings.Contains(body, "http://127.0.0.1:8085/verify") ||
		!strings.Contains(body, "http://localhost:9200/audit") {
		t.Fatalf("integrations roundtrip = %d, body = %s", recorder.Code, body)
	}
}

func TestDiscoveredCompaniesMatchesEngineContract(t *testing.T) {
	t.Parallel()

	repository := newProspectStubRepository()
	repository.businesses = prospectTestBusinesses()
	// The boundary is dormant by default; this test verifies the awake
	// contract, so it stores the explicit opt-in first.
	repository.settings[settingProspectFutureIntegrations] = futureIntegrationsEnabledValue
	_, mux := newProspectTestServer(t, repository)

	// The Engine boundary requires a job scope.
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/prospects/discovered", http.NoBody))
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("discovered without job_id = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/prospects/discovered?job_id=job-1", http.NoBody))
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK {
		t.Fatalf("discovered = %d, body = %s", recorder.Code, body)
	}
	for _, literal := range []string{`"providerCompanyId"`, `"rawPayload"`, `"website_status"`, `"sourceUrl"`} {
		if !strings.Contains(body, literal) {
			t.Fatalf("discovered body lacks %s: %s", literal, body)
		}
	}
	if repository.lastSearch.JobID != "job-1" {
		t.Fatalf("search job filter = %q, want job-1", repository.lastSearch.JobID)
	}

	var envelope struct {
		Data []DiscoveredCompany `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil || len(envelope.Data) != 2 {
		t.Fatalf("discovered decode = %v, companies = %#v", err, envelope.Data)
	}
	first, second := envelope.Data[0], envelope.Data[1]
	if first.ProviderCompanyID != "place-1" || first.Confidence != 1.0 || first.Domain != "harbordental.com" {
		t.Fatalf("first company = %#v", first)
	}
	if len(first.Meta.RawPayload.Emails) != 1 || first.Meta.RawPayload.Emails[0] != "hello@harbordental.com" {
		t.Fatalf("first company emails = %#v", first.Meta.RawPayload.Emails)
	}
	if second.ProviderCompanyID != "biz-2" || second.Confidence != 0.7 {
		t.Fatalf("second company = %#v", second)
	}
	if second.Meta.RawPayload.WebsiteStatus != prospect.StatusNoWebsite {
		t.Fatalf("second company status = %q, want %s", second.Meta.RawPayload.WebsiteStatus, prospect.StatusNoWebsite)
	}

	// The limit parameter bounds the page.
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/prospects/discovered?job_id=job-1&limit=1", http.NoBody))
	envelope.Data = nil
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil || len(envelope.Data) != 1 {
		t.Fatalf("limited discovered = %v, companies = %#v", err, envelope.Data)
	}
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/prospects/discovered?job_id=job-1&limit=99999", http.NoBody))
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("oversized limit = %d", recorder.Code)
	}

	// Without settings storage the opt-in cannot exist, so the dormant gate
	// answers before the result-store capability is even consulted.
	_, bareMux := newProspectTestServer(t, &fixedJobRepository{})
	recorder = httptest.NewRecorder()
	bareMux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/prospects/discovered?job_id=job-1", http.NoBody))
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), futureIntegrationsDisabledCode) {
		t.Fatalf("discovered without result store = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestProspectQueryGenerationUsesSampleZIPs(t *testing.T) {
	t.Parallel()

	server, mux := newProspectTestServer(t, newProspectStubRepository())

	form := url.Values{"top_n": {"5"}, "synonyms": {"dentist\nplumber"}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/prospects/queries", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("queries without CSRF = %d", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/prospects/queries", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("queries = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data ProspectQueryPlan `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("queries decode: %v", err)
	}
	plan := envelope.Data
	if len(plan.Queries) == 0 || plan.ZIPCount != 5 || plan.Centre[0] == 0 || plan.Centre[1] == 0 {
		t.Fatalf("query plan = %#v", plan)
	}

	// Synonyms are required.
	empty := url.Values{"top_n": {"5"}, "synonyms": {" \n "}}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/prospects/queries", strings.NewReader(empty.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("queries without synonyms = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	// A multipart upload replaces the sample ZIP areas.
	var multipartBody bytes.Buffer
	multipartWriter := multipart.NewWriter(&multipartBody)
	_ = multipartWriter.WriteField("synonyms", "dentist")
	_ = multipartWriter.WriteField("top_n", "10")
	filePart, err := multipartWriter.CreateFormFile("zips_csv", "zips.csv")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = filePart.Write([]byte("zip,city,state,latitude,longitude,population\n33101,Miami,FL,25.774,-80.193,12000\n33125,Miami,FL,25.784,-80.237,49000\n"))
	if err := multipartWriter.Close(); err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/prospects/queries", &multipartBody)
	request.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("CSV queries = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	envelope.Data = ProspectQueryPlan{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("CSV queries decode: %v", err)
	}
	if envelope.Data.ZIPCount != 2 || len(envelope.Data.Queries) != 2 {
		t.Fatalf("CSV query plan = %#v", envelope.Data)
	}
}

func TestDiscoveredCompaniesExportFormatWritesVerifiedJSONL(t *testing.T) {
	t.Parallel()

	extension, ok := exportExtension("discovered_companies")
	if !ok || extension != "jsonl" {
		t.Fatalf("discovered_companies extension = %q, %v", extension, ok)
	}

	path := filepath.Join(t.TempDir(), "companies.jsonl")
	writer, err := newExportPartWriter(path, "discovered_companies", defaultExportColumns(), ExportBuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, business := range prospectTestBusinesses() {
		if err := writer.Add(exportDataRow{Business: business}); err != nil {
			writer.Abort()
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := verifyExportFile("discovered_companies", path); err != nil {
		t.Fatalf("verify discovered_companies: %v", err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	if len(lines) != 2 {
		t.Fatalf("JSONL lines = %d: %s", len(lines), contents)
	}
	for _, literal := range []string{`"providerCompanyId":"place-1"`, `"rawPayload"`, `"website_status"`} {
		if !strings.Contains(lines[0], literal) {
			t.Fatalf("JSONL line lacks %s: %s", literal, lines[0])
		}
	}
	var company DiscoveredCompany
	if err := json.Unmarshal([]byte(lines[1]), &company); err != nil {
		t.Fatalf("JSONL line 2 decode: %v", err)
	}
	if company.ProviderCompanyID != "biz-2" || company.Confidence != 0.7 {
		t.Fatalf("JSONL company = %#v", company)
	}

	// The verifier rejects lines that break the Engine contract.
	invalid := filepath.Join(t.TempDir(), "invalid.jsonl")
	if err := os.WriteFile(invalid, []byte("{\"name\":\"missing id\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyDiscoveredCompaniesExport(invalid); err == nil {
		t.Fatal("verifier accepted a line without providerCompanyId")
	}
}

func TestProspectExportColumnsComputeFromBusinessRow(t *testing.T) {
	t.Parallel()

	row := exportDataRow{Business: BusinessResult{
		ID: "biz-solo", Name: "Solo Cafe", PrimaryCategory: "Cafe", City: "Miami",
	}}

	status, err := exportColumnValue(row, "prospect_status")
	if err != nil || status != prospect.StatusNoWebsite {
		t.Fatalf("prospect_status = %v, %v", status, err)
	}
	score, err := exportColumnValue(row, "prospect_score")
	if err != nil || score.(float64) <= 0 {
		t.Fatalf("prospect_score = %v, %v", score, err)
	}
	tier, err := exportColumnValue(row, "prospect_tier")
	if err != nil || tier.(string) == "" {
		t.Fatalf("prospect_tier = %v, %v", tier, err)
	}
	reasons, err := exportColumnValue(row, "prospect_reasons")
	if err != nil || !json.Valid(reasons.(json.RawMessage)) ||
		!strings.Contains(string(reasons.(json.RawMessage)), prospect.StatusNoWebsite) {
		t.Fatalf("prospect_reasons = %s, %v", reasons, err)
	}
	opener, err := exportColumnValue(row, "call_opener")
	if err != nil || !strings.Contains(opener.(string), "Solo Cafe") {
		t.Fatalf("call_opener = %v, %v", opener, err)
	}
}

func TestReadTimeSignalsTreatUnknownWebsiteStatusAsUnaudited(t *testing.T) {
	t.Parallel()

	// The placeholder website_status "unknown" means "never audited"; it must
	// not masquerade as audit evidence, or every live-but-unaudited site
	// would classify as DEAD at read time.
	signals := prospectSignalsFromBusiness(BusinessResult{
		Website:       "http://marinagrinsdental.com",
		WebsiteStatus: "unknown",
	})

	if signals.AuditPerformed {
		t.Fatal("website_status \"unknown\" was treated as a performed audit")
	}

	status, conclusive := prospect.Classify(signals)
	if conclusive || status != "" {
		t.Fatalf("unaudited custom domain classified as %q (conclusive=%v), want inconclusive", status, conclusive)
	}

	// A genuine audit outcome still concludes.
	audited := prospectSignalsFromBusiness(BusinessResult{
		Website:       "http://marinagrinsdental.com",
		WebsiteStatus: "error",
	})

	status, conclusive = prospect.Classify(audited)
	if !conclusive || status != prospect.StatusDead {
		t.Fatalf("audited error site = %q (conclusive=%v), want DEAD", status, conclusive)
	}
}
