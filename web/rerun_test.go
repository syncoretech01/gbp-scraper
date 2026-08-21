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

// campaignTestRepository is a JobRepository that also stores campaign
// lineage, so the rescan service can be exercised without SQLite.
type campaignTestRepository struct {
	jobs   map[string]Job
	states map[string]jobruntime.State
	links  map[string]JobCampaignLink
	// createErr, when set, fails the next job creation.
	createErr error
	// creates counts how many jobs were persisted.
	creates int
}

func newCampaignTestRepository(seed ...Job) *campaignTestRepository {
	repository := &campaignTestRepository{
		jobs:   make(map[string]Job),
		states: make(map[string]jobruntime.State),
		links:  make(map[string]JobCampaignLink),
	}

	for _, job := range seed {
		repository.jobs[job.ID] = job
	}

	return repository
}

func (r *campaignTestRepository) Get(_ context.Context, id string) (Job, error) {
	job, ok := r.jobs[id]
	if !ok {
		return Job{}, ErrNotFound
	}

	return job, nil
}

func (r *campaignTestRepository) Create(_ context.Context, job *Job) error {
	if r.createErr != nil {
		return r.createErr
	}

	r.creates++
	r.jobs[job.ID] = *job

	return nil
}

func (r *campaignTestRepository) CreateWithState(
	_ context.Context,
	job *Job,
	state jobruntime.State,
) error {
	if r.createErr != nil {
		return r.createErr
	}

	r.creates++
	r.jobs[job.ID] = *job
	r.states[job.ID] = state

	return nil
}

func (r *campaignTestRepository) Delete(_ context.Context, id string) error {
	delete(r.jobs, id)

	return nil
}

func (r *campaignTestRepository) Update(_ context.Context, job *Job) error {
	r.jobs[job.ID] = *job

	return nil
}

func (r *campaignTestRepository) Select(context.Context, SelectParams) ([]Job, error) {
	jobs := make([]Job, 0, len(r.jobs))
	for _, job := range r.jobs {
		jobs = append(jobs, job)
	}

	return jobs, nil
}

func (r *campaignTestRepository) SaveJobCampaignLink(_ context.Context, link JobCampaignLink) error {
	for _, existing := range r.links {
		if existing.JobID == link.JobID || link.IdempotencyKey == "" {
			continue
		}

		if existing.CampaignID == link.CampaignID && existing.IdempotencyKey == link.IdempotencyKey {
			return errors.New("duplicate campaign idempotency key")
		}
	}

	r.links[link.JobID] = link

	return nil
}

func (r *campaignTestRepository) GetJobCampaignLink(
	_ context.Context,
	jobID string,
) (JobCampaignLink, error) {
	link, ok := r.links[jobID]
	if !ok {
		return JobCampaignLink{}, ErrCampaignNotFound
	}

	return link, nil
}

func (r *campaignTestRepository) CampaignLinks(
	_ context.Context,
	campaignID string,
) ([]JobCampaignLink, error) {
	links := make([]JobCampaignLink, 0, len(r.links))

	for _, link := range r.links {
		if link.CampaignID == campaignID {
			links = append(links, link)
		}
	}

	for outer := range links {
		for inner := outer + 1; inner < len(links); inner++ {
			if links[inner].Generation < links[outer].Generation {
				links[outer], links[inner] = links[inner], links[outer]
			}
		}
	}

	return links, nil
}

func (r *campaignTestRepository) FindCampaignIdempotencyKey(
	_ context.Context,
	campaignID, key string,
) (JobCampaignLink, error) {
	if key == "" {
		return JobCampaignLink{}, ErrCampaignNotFound
	}

	for _, link := range r.links {
		if link.CampaignID == campaignID && link.IdempotencyKey == key {
			return link, nil
		}
	}

	return JobCampaignLink{}, ErrCampaignNotFound
}

func rerunSourceJob() Job {
	return Job{
		ID:     "6a3c7f6e-0f27-4a4a-9d4c-0f2a1b3c4d5e",
		Name:   "Austin plumbers",
		Date:   time.Unix(1_700_000_000, 0).UTC(),
		Status: StatusOK,
		Data: JobData{
			Keywords: []string{"plumber in Austin TX 78701", "plumber in Austin TX 78702"},
			Lang:     "en", Zoom: 14, Depth: 10, MaxTime: 30 * time.Minute,
			Coverage: &CoverageOptions{AutoStop: true, MaxExpansions: 12, ExpansionMinNew: 8},
		},
	}
}

func TestRerunCreatesALinkedRescanWithoutTouchingTheSource(t *testing.T) {
	t.Parallel()

	source := rerunSourceJob()
	repository := newCampaignTestRepository(source)
	service := NewService(repository, t.TempDir())

	rerun, err := service.RerunJob(context.Background(), RerunRequest{
		SourceJobID: source.ID, Mode: RerunModeChangedOnly,
	})
	if err != nil {
		t.Fatalf("RerunJob: %v", err)
	}

	if rerun.Job.ID == source.ID || rerun.Job.ID == "" {
		t.Fatalf("rescan job ID = %q, want a distinct new ID", rerun.Job.ID)
	}

	if rerun.Job.Name != "Austin plumbers (rescan 1)" {
		t.Fatalf("rescan name = %q", rerun.Job.Name)
	}

	// The whole campaign shape is carried over unchanged apart from the
	// rescan mode.
	if len(rerun.Job.Data.Keywords) != 2 || rerun.Job.Data.Coverage == nil ||
		rerun.Job.Data.Coverage.MaxExpansions != 12 {
		t.Fatalf("rescan data = %#v", rerun.Job.Data)
	}

	if rerun.Job.Data.IncrementalMode != IncrementalModeNewChanged {
		t.Fatalf("rescan incremental mode = %q, want %q",
			rerun.Job.Data.IncrementalMode, IncrementalModeNewChanged)
	}

	if rerun.Link.SourceJobID != source.ID || rerun.Link.CampaignID != source.ID ||
		rerun.Link.RootJobID != source.ID || rerun.Link.Generation != 1 {
		t.Fatalf("lineage = %#v", rerun.Link)
	}

	if rerun.State != string(jobruntime.StateQueued) || rerun.Reused {
		t.Fatalf("rescan state = %q reused = %v", rerun.State, rerun.Reused)
	}

	// The source job is untouched: same configuration, same status, and no
	// incremental mode was pushed onto it.
	stored, err := service.Get(context.Background(), source.ID)
	if err != nil {
		t.Fatalf("read source job: %v", err)
	}

	if stored.Status != StatusOK || stored.Data.IncrementalMode != "" || stored.Name != source.Name {
		t.Fatalf("source job was disturbed: %#v", stored)
	}
}

func TestRerunModesMapOntoTheEnginesCollectionModes(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		RerunModeFull:        "",
		RerunModeNewOnly:     IncrementalModeNewOnly,
		RerunModeChangedOnly: IncrementalModeNewChanged,
	}

	for mode, want := range cases {
		source := rerunSourceJob()
		repository := newCampaignTestRepository(source)
		service := NewService(repository, t.TempDir())

		rerun, err := service.RerunJob(context.Background(), RerunRequest{
			SourceJobID: source.ID, Mode: mode,
		})
		if err != nil {
			t.Fatalf("RerunJob(%q): %v", mode, err)
		}

		if rerun.Job.Data.IncrementalMode != want {
			t.Fatalf("mode %q produced incremental mode %q, want %q",
				mode, rerun.Job.Data.IncrementalMode, want)
		}
	}

	source := rerunSourceJob()
	service := NewService(newCampaignTestRepository(source), t.TempDir())

	if _, err := service.RerunJob(context.Background(), RerunRequest{
		SourceJobID: source.ID, Mode: "sideways",
	}); !errors.Is(err, ErrInvalidRerun) {
		t.Fatalf("unknown mode error = %v, want ErrInvalidRerun", err)
	}
}

func TestRerunIsIdempotentUnderARepeatedKey(t *testing.T) {
	t.Parallel()

	source := rerunSourceJob()
	repository := newCampaignTestRepository(source)
	service := NewService(repository, t.TempDir())

	request := RerunRequest{
		SourceJobID: source.ID, Mode: RerunModeNewOnly, IdempotencyKey: "nightly-2026-08-21",
	}

	first, err := service.RerunJob(context.Background(), request)
	if err != nil {
		t.Fatalf("first rescan: %v", err)
	}

	second, err := service.RerunJob(context.Background(), request)
	if err != nil {
		t.Fatalf("repeated rescan: %v", err)
	}

	if !second.Reused || second.Job.ID != first.Job.ID {
		t.Fatalf("repeated rescan = %#v, want the first run reused", second.Link)
	}

	if repository.creates != 1 {
		t.Fatalf("jobs created = %d, want exactly 1 for a repeated key", repository.creates)
	}

	// A different key genuinely starts another run.
	third, err := service.RerunJob(context.Background(), RerunRequest{
		SourceJobID: source.ID, Mode: RerunModeNewOnly, IdempotencyKey: "nightly-2026-08-22",
	})
	if err != nil {
		t.Fatalf("second key rescan: %v", err)
	}

	if third.Reused || third.Job.ID == first.Job.ID {
		t.Fatalf("a distinct key reused an existing run: %#v", third.Link)
	}
}

func TestRerunGenerationsChainThroughTheCampaign(t *testing.T) {
	t.Parallel()

	source := rerunSourceJob()
	repository := newCampaignTestRepository(source)
	service := NewService(repository, t.TempDir())

	first, err := service.RerunJob(context.Background(), RerunRequest{
		SourceJobID: source.ID, Mode: RerunModeFull,
	})
	if err != nil {
		t.Fatalf("first rescan: %v", err)
	}

	second, err := service.RerunJob(context.Background(), RerunRequest{
		SourceJobID: first.Job.ID, Mode: RerunModeNewOnly,
	})
	if err != nil {
		t.Fatalf("second rescan: %v", err)
	}

	if second.Link.Generation != 2 || second.Link.SourceJobID != first.Job.ID ||
		second.Link.CampaignID != source.ID || second.Link.RootJobID != source.ID {
		t.Fatalf("second generation lineage = %#v", second.Link)
	}

	// A rescan of a rescan keeps one readable name rather than stacking
	// suffixes.
	if second.Job.Name != "Austin plumbers (rescan 2)" {
		t.Fatalf("second generation name = %q", second.Job.Name)
	}

	campaign, err := service.JobCampaignOf(context.Background(), second.Job.ID)
	if err != nil {
		t.Fatalf("read campaign: %v", err)
	}

	if campaign.CampaignID != source.ID || len(campaign.Jobs) != 3 {
		t.Fatalf("campaign = %#v", campaign)
	}

	for index, want := range []string{source.ID, first.Job.ID, second.Job.ID} {
		if campaign.Jobs[index].JobID != want {
			t.Fatalf("campaign job %d = %q, want %q", index, campaign.Jobs[index].JobID, want)
		}
	}

	ids, err := service.CampaignJobIDs(context.Background(), source.ID)
	if err != nil || len(ids) != 3 {
		t.Fatalf("campaign job IDs = %v (%v)", ids, err)
	}
}

func TestJobCampaignOfReportsAStandaloneJobAsItsOwnCampaign(t *testing.T) {
	t.Parallel()

	source := rerunSourceJob()
	service := NewService(newCampaignTestRepository(source), t.TempDir())

	campaign, err := service.JobCampaignOf(context.Background(), source.ID)
	if err != nil {
		t.Fatalf("JobCampaignOf: %v", err)
	}

	if campaign.CampaignID != source.ID || len(campaign.Jobs) != 1 ||
		campaign.Jobs[0].Generation != 0 {
		t.Fatalf("standalone campaign = %#v", campaign)
	}
}

func TestRerunRejectsUnsupportedRepositoriesAndBadKeys(t *testing.T) {
	t.Parallel()

	plain := NewService(&fixedJobRepository{job: rerunSourceJob()}, t.TempDir())
	if _, err := plain.RerunJob(context.Background(), RerunRequest{
		SourceJobID: rerunSourceJob().ID, Mode: RerunModeFull,
	}); !errors.Is(err, ErrCampaignUnsupported) {
		t.Fatalf("plain repository error = %v, want ErrCampaignUnsupported", err)
	}

	source := rerunSourceJob()
	service := NewService(newCampaignTestRepository(source), t.TempDir())

	if _, err := service.RerunJob(context.Background(), RerunRequest{
		SourceJobID: source.ID, Mode: RerunModeFull, IdempotencyKey: "bad key!",
	}); !errors.Is(err, ErrInvalidRerun) {
		t.Fatalf("invalid key error = %v, want ErrInvalidRerun", err)
	}

	if _, err := service.RerunJob(context.Background(), RerunRequest{
		SourceJobID: source.ID, Mode: RerunModeFull, Name: strings.Repeat("x", 200),
	}); !errors.Is(err, ErrInvalidRerun) {
		t.Fatalf("oversized name error = %v, want ErrInvalidRerun", err)
	}
}

func TestRerunAPIRequiresCSRFAndReturnsTheLineage(t *testing.T) {
	t.Parallel()

	source := rerunSourceJob()
	repository := newCampaignTestRepository(source)

	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body := `{"mode":"new-only","idempotency_key":"api-key-1"}`

	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+source.ID+"/rerun", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("rescan without a CSRF token = %d, want 403", recorder.Code)
	}

	if repository.creates != 0 {
		t.Fatalf("a rejected request created %d job(s)", repository.creates)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+source.ID+"/rerun", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	recorder = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("rescan = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Data struct {
			JobID       string `json:"job_id"`
			CampaignID  string `json:"campaign_id"`
			RootJobID   string `json:"root_job_id"`
			SourceJobID string `json:"source_job_id"`
			Mode        string `json:"mode"`
			State       string `json:"state"`
			Generation  int    `json:"generation"`
			Reused      bool   `json:"reused"`
		} `json:"data"`
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode rescan response: %v", err)
	}

	if response.Data.JobID == "" || response.Data.CampaignID != source.ID ||
		response.Data.SourceJobID != source.ID || response.Data.Mode != RerunModeNewOnly ||
		response.Data.Generation != 1 || response.Data.Reused {
		t.Fatalf("rescan response = %#v", response.Data)
	}

	// A repeat carrying the same key answers 200 with the same run.
	request = httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+source.ID+"/rerun", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	recorder = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"reused":true`) {
		t.Fatalf("repeated rescan = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	if repository.creates != 1 {
		t.Fatalf("jobs created = %d, want 1", repository.creates)
	}

	// The lineage is readable back through the campaign endpoint.
	request = httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+response.Data.JobID+"/campaign", nil)
	recorder = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), source.ID) {
		t.Fatalf("campaign readback = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestRerunAPIRejectsUnknownModesAndJobs(t *testing.T) {
	t.Parallel()

	source := rerunSourceJob()
	server, err := New(NewService(newCampaignTestRepository(source), t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+source.ID+"/rerun",
		strings.NewReader(`{"mode":"sideways"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown mode = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	missing := "11111111-2222-4333-8444-555555555555"
	request = httptest.NewRequest(http.MethodPost, "/api/v1/jobs/"+missing+"/rerun",
		strings.NewReader(`{"mode":"full"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", server.csrfToken)
	recorder = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing job = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestValidRerunMode(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{RerunModeFull, RerunModeNewOnly, RerunModeChangedOnly} {
		if !ValidRerunMode(mode) {
			t.Fatalf("ValidRerunMode(%q) = false", mode)
		}
	}

	for _, mode := range []string{"", "new_only", "changed", "FULL"} {
		if ValidRerunMode(mode) {
			t.Fatalf("ValidRerunMode(%q) = true", mode)
		}
	}
}
