package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

func TestAPIJobControlReturnsCanonicalDecision(t *testing.T) {
	t.Parallel()

	const jobID = "66666666-6666-6666-6666-666666666666"
	repo := &fakeLifecycleRepository{
		job:     Job{ID: jobID, Name: "dentists", Status: StatusPending},
		runtime: JobRuntime{JobID: jobID, State: jobruntime.StateQueued, Stage: jobruntime.StagePreparingQueries},
	}
	srv, err := New(NewService(repo, t.TempDir()), ":0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+jobID+"/pause", http.NoBody)
	req.SetPathValue("id", jobID)
	req = requestWithID(req)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()

	srv.apiJobControl(rec, req, jobruntime.ControlPause)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	for _, expected := range []string{`"state":"paused"`, `"disposition":"applied"`, `"message":"queued job paused"`} {
		if !strings.Contains(rec.Body.String(), expected) {
			t.Fatalf("response missing %s: %s", expected, rec.Body.String())
		}
	}
}

func TestAPIJobControlRequiresCSRFForBrowserOrigin(t *testing.T) {
	t.Parallel()

	const jobID = "77777777-7777-7777-7777-777777777777"
	repo := &fakeLifecycleRepository{
		job:     Job{ID: jobID, Status: StatusPending},
		runtime: JobRuntime{JobID: jobID, State: jobruntime.StateQueued},
	}
	srv, err := New(NewService(repo, t.TempDir()), ":0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+jobID+"/cancel", http.NoBody)
	req.SetPathValue("id", jobID)
	req = requestWithID(req)
	req.Header.Set("Origin", "https://malicious.example")
	rec := httptest.NewRecorder()

	srv.apiJobControl(rec, req, jobruntime.ControlCancel)

	if rec.Code != http.StatusForbidden || repo.controlCalls != 0 {
		t.Fatalf("status = %d, control calls = %d", rec.Code, repo.controlCalls)
	}
}

func TestAPIJobEventsReplaysDurableEvents(t *testing.T) {
	t.Parallel()

	const jobID = "88888888-8888-8888-8888-888888888888"
	ctx, cancel := context.WithCancel(context.Background())
	repo := &fakeLifecycleRepository{
		job: Job{ID: jobID, Status: StatusWorking},
		runtime: JobRuntime{
			JobID: jobID, State: jobruntime.StateRunning, Stage: jobruntime.StageSearchingMaps,
		},
		events: []JobEvent{{
			ID: 4, JobID: jobID, Type: "progress", Severity: "info",
			Stage: jobruntime.StageSearchingMaps, Message: "20 places found", OccurredAt: time.Now().UTC(),
		}},
		afterEvents: cancel,
	}
	srv, err := New(NewService(repo, t.TempDir()), ":0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+jobID+"/events?after=3", http.NoBody).WithContext(ctx)
	req.SetPathValue("id", jobID)
	req = requestWithID(req)
	rec := httptest.NewRecorder()

	srv.apiJobEvents(rec, req)

	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type = %q", rec.Header().Get("Content-Type"))
	}

	body := rec.Body.String()
	for _, expected := range []string{"event: snapshot", "id: 4", "event: progress", "20 places found"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("stream missing %q: %s", expected, body)
		}
	}
}

func TestEventCursorPrefersQueryAndValidates(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/events?after=12", http.NoBody)
	req.Header.Set("Last-Event-ID", "9")

	cursor, err := eventCursor(req)
	if err != nil || cursor != 12 {
		t.Fatalf("cursor = %d, error = %v", cursor, err)
	}

	bad := httptest.NewRequest(http.MethodGet, "/events?after=-1", http.NoBody)
	if _, err := eventCursor(bad); err == nil {
		t.Fatal("expected invalid cursor error")
	}
}

type fakeLifecycleRepository struct {
	job          Job
	runtime      JobRuntime
	events       []JobEvent
	afterEvents  context.CancelFunc
	controlCalls int
}

func (r *fakeLifecycleRepository) Get(_ context.Context, id string) (Job, error) {
	if id != r.job.ID {
		return Job{}, ErrLifecycleNotFound
	}

	return r.job, nil
}

func (r *fakeLifecycleRepository) Create(context.Context, *Job) error   { return nil }
func (r *fakeLifecycleRepository) Delete(context.Context, string) error { return nil }
func (r *fakeLifecycleRepository) Update(context.Context, *Job) error   { return nil }

func (r *fakeLifecycleRepository) Select(context.Context, SelectParams) ([]Job, error) {
	return []Job{r.job}, nil
}

func (r *fakeLifecycleRepository) CreateWithState(_ context.Context, job *Job, state jobruntime.State) error {
	r.job = *job
	r.runtime = JobRuntime{JobID: job.ID, State: state}

	return nil
}

func (r *fakeLifecycleRepository) GetRuntime(_ context.Context, id string) (JobRuntime, error) {
	if id != r.runtime.JobID {
		return JobRuntime{}, ErrLifecycleNotFound
	}

	return r.runtime, nil
}

func (r *fakeLifecycleRepository) ApplyControl(
	_ context.Context,
	id string,
	control jobruntime.Control,
) (JobRuntime, jobruntime.ControlDecision, error) {
	if id != r.runtime.JobID {
		return JobRuntime{}, jobruntime.ControlDecision{}, ErrLifecycleNotFound
	}

	r.controlCalls++
	decision, err := jobruntime.DecideControl(r.runtime.State, r.runtime.RequestedStop, control)
	if err != nil {
		return JobRuntime{}, jobruntime.ControlDecision{}, err
	}

	if err := decision.Error(); err != nil {
		return JobRuntime{}, decision, err
	}

	if decision.Changed() {
		r.runtime.State = decision.NextState
		r.runtime.RequestedStop = decision.RequestedStop
		r.runtime.StateVersion++
	}

	return r.runtime, decision, nil
}

func (r *fakeLifecycleRepository) SetOutcome(
	_ context.Context,
	id string,
	outcome jobruntime.Outcome,
	message string,
) (JobRuntime, error) {
	if id != r.runtime.JobID {
		return JobRuntime{}, ErrLifecycleNotFound
	}

	r.runtime.State = outcome.State
	r.runtime.OutcomeReason = outcome.Reason
	r.runtime.Message = message

	return r.runtime, nil
}

func (r *fakeLifecycleRepository) EventsAfter(
	ctx context.Context,
	id string,
	after int64,
	limit int,
) ([]JobEvent, error) {
	if id != r.runtime.JobID {
		return nil, ErrLifecycleNotFound
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	result := make([]JobEvent, 0, min(limit, len(r.events)))
	for _, event := range r.events {
		if event.ID > after && len(result) < limit {
			result = append(result, event)
		}
	}

	if r.afterEvents != nil {
		r.afterEvents()
		r.afterEvents = nil
	}

	return result, nil
}

var _ LifecycleRepository = (*fakeLifecycleRepository)(nil)
var _ JobRepository = (*fakeLifecycleRepository)(nil)

func TestFakeLifecycleRepositoryRejectsInvalidControl(t *testing.T) {
	t.Parallel()

	repo := &fakeLifecycleRepository{runtime: JobRuntime{JobID: "job", State: jobruntime.StateCompleted}}
	_, _, err := repo.ApplyControl(context.Background(), "job", jobruntime.ControlCancel)
	if !errors.Is(err, jobruntime.ErrControlRejected) {
		t.Fatalf("error = %v", err)
	}
}
