package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

func TestAPIDuplicateJobCreatesDraftWithoutLeakingConfiguration(t *testing.T) {
	t.Parallel()

	const jobID = "99999999-9999-9999-9999-999999999999"
	repo := &fakeLifecycleRepository{
		job: Job{
			ID: jobID, Name: "San Francisco dentists", Date: time.Now().UTC(), Status: StatusOK,
			Data: JobData{Proxies: []string{"https://user:top-secret@proxy.test:443"}},
		},
		runtime: JobRuntime{JobID: jobID, State: jobruntime.StateCompleted},
	}
	srv, err := New(NewService(repo, t.TempDir()), ":0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+jobID+"/duplicate", http.NoBody)
	req.SetPathValue("id", jobID)
	req = requestWithID(req)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()

	srv.apiDuplicateJob(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	if repo.job.ID == jobID || repo.runtime.State != jobruntime.StateDraft {
		t.Fatalf("duplicated job = %+v, runtime = %+v", repo.job, repo.runtime)
	}

	if !strings.HasSuffix(repo.job.Name, "(copy)") {
		t.Fatalf("copy name = %q", repo.job.Name)
	}

	if strings.Contains(rec.Body.String(), "top-secret") || strings.Contains(rec.Body.String(), "proxy.test") {
		t.Fatalf("response leaked configuration: %s", rec.Body.String())
	}
}
