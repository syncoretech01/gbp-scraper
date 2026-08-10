package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAPIJobProgressSummarizesLegacyCSVWithoutLeakingProxies(t *testing.T) {
	t.Parallel()

	const jobID = "33333333-3333-3333-3333-333333333333"

	dir := t.TempDir()
	writeCSV(t, dir, jobID, strings.Join([]string{
		"place_id,title,address,website,phone,emails",
		"one,Alpha,1 Main St,https://alpha.test,+1 555 1000,[hello@alpha.test]",
		"one,Alpha duplicate,1 Main St,,,[]",
	}, "\n"))

	repo := &fixedJobRepository{job: Job{
		ID:     jobID,
		Name:   "San Francisco dentists",
		Date:   time.Now().UTC(),
		Status: StatusOK,
		Data: JobData{
			Keywords: []string{"dentists"},
			Lang:     "en",
			Zoom:     12,
			Lat:      "37.7749",
			Lon:      "-122.4194",
			Radius:   10000,
			Depth:    10,
			Email:    true,
			MaxTime:  10 * time.Minute,
			Proxies:  []string{"https://operator:super-secret@proxy.test:443"},
		},
	}}

	srv, err := New(NewService(repo, dir), ":0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+jobID+"/progress", http.NoBody)
	req.SetPathValue("id", jobID)
	req = requestWithID(req)
	rec := httptest.NewRecorder()
	srv.apiJobProgress(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if strings.Contains(body, "super-secret") || strings.Contains(body, "proxy.test") {
		t.Fatalf("response leaked proxy secret: %s", body)
	}

	var envelope struct {
		Data jobProgressDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if envelope.Data.Results.Rows != 2 || envelope.Data.Results.UniqueBusinesses != 1 {
		t.Fatalf("results = %+v", envelope.Data.Results)
	}

	if len(envelope.Data.Warnings) != 2 {
		t.Fatalf("warnings = %v", envelope.Data.Warnings)
	}
}

func TestAPIDashboardAggregatesLocalResults(t *testing.T) {
	t.Parallel()

	const jobID = "44444444-4444-4444-4444-444444444444"

	dir := t.TempDir()
	writeCSV(t, dir, jobID, "place_id,title,website,phone,emails\none,Alpha,https://alpha.test,+1 555,[a@alpha.test]\n")

	repo := &fixedJobRepository{job: Job{ID: jobID, Name: "dentists", Status: StatusOK}}
	srv, err := New(NewService(repo, dir), ":0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptest.NewRecorder()
	srv.apiDashboard(rec, httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", http.NoBody))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var envelope struct {
		Data dashboardDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if envelope.Data.Jobs != 1 || envelope.Data.UniqueBusinesses != 1 || envelope.Data.WithEmail != 1 {
		t.Fatalf("dashboard = %+v", envelope.Data)
	}

	if envelope.Data.JobStates["completed"] != 1 {
		t.Fatalf("states = %+v", envelope.Data.JobStates)
	}

}

type fixedJobRepository struct {
	job Job
}

func (r *fixedJobRepository) Get(_ context.Context, id string) (Job, error) {
	if id != r.job.ID {
		return Job{}, ErrPlacesNotFound
	}

	return r.job, nil
}

func (r *fixedJobRepository) Create(context.Context, *Job) error   { return nil }
func (r *fixedJobRepository) Delete(context.Context, string) error { return nil }
func (r *fixedJobRepository) Update(context.Context, *Job) error   { return nil }

func (r *fixedJobRepository) Select(context.Context, SelectParams) ([]Job, error) {
	return []Job{r.job}, nil
}
