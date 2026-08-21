package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The label and note capabilities are declared here rather than in the monitor
// fixture so the monitor tests keep exercising the "no label storage" path and
// these exercise the storage path.
func (r *monitorSpecRepository) SetJobLabels(_ context.Context, jobID string, labels JobLabels) error {
	labels.JobID = jobID
	r.labels = labels

	return nil
}

func (r *monitorSpecRepository) JobLabels(_ context.Context, jobID string) (JobLabels, error) {
	labels := r.labels
	labels.JobID = jobID
	if labels.Tags == nil {
		labels.Tags = []string{}
	}

	return labels, nil
}

func (r *monitorSpecRepository) AllJobLabels(context.Context) (map[string]JobLabels, error) {
	return map[string]JobLabels{r.job.ID: r.labels}, nil
}

func (r *monitorSpecRepository) RenameJob(_ context.Context, _, name string) error {
	r.job.Name = name

	return nil
}

func (r *monitorSpecRepository) SetJobArchived(_ context.Context, _ string, archived bool) error {
	r.organisation.Archived = archived

	return nil
}

func (r *monitorSpecRepository) SetJobNotes(_ context.Context, _, notes string) error {
	r.organisation.Notes = notes

	return nil
}

func (r *monitorSpecRepository) JobOrganisation(_ context.Context, jobID string) (JobOrganisation, error) {
	organisation := r.organisation
	organisation.JobID = jobID
	organisation.Folder = r.labels.Folder

	return organisation, nil
}

func (r *monitorSpecRepository) ArchivedJobIDs(context.Context) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}

func TestNormalizeJobLabelsTrimsDeduplicatesAndBounds(t *testing.T) {
	t.Parallel()

	labels, err := NormalizeJobLabels(JobLabels{
		Folder: "  Q3 campaigns  ",
		Owner:  " Dana ",
		Tags:   []string{"austin", "Austin", " plumbing ", "", "   "},
	})
	if err != nil {
		t.Fatalf("NormalizeJobLabels: %v", err)
	}

	if labels.Folder != "Q3 campaigns" || labels.Owner != "Dana" {
		t.Fatalf("normalized labels = %#v", labels)
	}
	if len(labels.Tags) != 2 || labels.Tags[0] != "austin" || labels.Tags[1] != "plumbing" {
		t.Fatalf("normalized tags = %#v", labels.Tags)
	}
}

func TestNormalizeJobLabelsRejectsUnusableValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		labels JobLabels
	}{
		{name: "control character in folder", labels: JobLabels{Folder: "a\nb"}},
		{name: "oversized owner", labels: JobLabels{Owner: strings.Repeat("o", MaximumJobOwnerLength+1)}},
		{name: "oversized tag", labels: JobLabels{Tags: []string{strings.Repeat("t", MaximumJobTagLength+1)}}},
		{name: "too many tags", labels: JobLabels{Tags: manyTags(MaximumJobTags + 1)}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if _, err := NormalizeJobLabels(test.labels); err == nil {
				t.Fatal("expected an invalid label error")
			}
		})
	}
}

func manyTags(count int) []string {
	tags := make([]string, 0, count)
	for index := range count {
		tags = append(tags, string(rune('a'+index))+"-tag")
	}

	return tags
}

func TestAPIJobLabelsAppliesFolderTagsOwnerAndNotes(t *testing.T) {
	t.Parallel()

	srv, repository := newMonitorSpecServer(t)

	form := url.Values{}
	form.Set("csrf_token", srv.csrfToken)
	form.Set("folder", "Q3 campaigns")
	form.Set("tags", "austin, plumbing, austin")
	form.Set("owner", "Dana")
	form.Set("notes", "Re-run after the grid change.")

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/jobs/"+monitorSpecJobID+"/labels?id="+monitorSpecJobID,
		strings.NewReader(form.Encode()),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")

	recorder := httptest.NewRecorder()
	srv.handleJobLabels(recorder, requestWithID(request))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	if repository.labels.Folder != "Q3 campaigns" || repository.labels.Owner != "Dana" {
		t.Fatalf("stored labels = %#v", repository.labels)
	}
	if len(repository.labels.Tags) != 2 {
		t.Fatalf("stored tags = %#v", repository.labels.Tags)
	}
	if repository.organisation.Notes != "Re-run after the grid change." {
		t.Fatalf("stored notes = %q", repository.organisation.Notes)
	}

	body := recorder.Body.String()
	for _, want := range []string{`"folder":"Q3 campaigns"`, `"owner":"Dana"`, `"austin"`, `"plumbing"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("label response missing %q: %s", want, body)
		}
	}
}

func TestAPIJobLabelsRejectsInvalidValuesAndMissingCSRF(t *testing.T) {
	t.Parallel()

	srv, _ := newMonitorSpecServer(t)

	invalid := url.Values{}
	invalid.Set("csrf_token", srv.csrfToken)
	invalid.Set("owner", strings.Repeat("o", MaximumJobOwnerLength+1))

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/jobs/"+monitorSpecJobID+"/labels?id="+monitorSpecJobID,
		strings.NewReader(invalid.Encode()),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")

	recorder := httptest.NewRecorder()
	srv.handleJobLabels(recorder, requestWithID(request))
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid owner status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	missing := url.Values{}
	missing.Set("folder", "Q3")
	unprotected := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/jobs/"+monitorSpecJobID+"/labels?id="+monitorSpecJobID,
		strings.NewReader(missing.Encode()),
	)
	unprotected.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	recorder = httptest.NewRecorder()
	srv.handleJobLabels(recorder, requestWithID(unprotected))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d", recorder.Code)
	}
}

func TestJobMonitorRendersTheLabelEditorWhenStorageExists(t *testing.T) {
	t.Parallel()

	srv, repository := newMonitorSpecServer(t)
	repository.labels = JobLabels{Folder: "Q3 campaigns", Owner: "Dana", Tags: []string{"austin", "plumbing"}}
	repository.organisation.Notes = "Watch the block rate."

	body := renderMonitor(t, srv, "")

	for _, want := range []string{
		`id="job-labels"`,
		`data-job-label-form`,
		`action="/api/v1/jobs/` + monitorSpecJobID + `/labels"`,
		`id="job-folder-input"`,
		`id="job-tags-input"`,
		`id="job-owner-input"`,
		`id="job-notes-input"`,
		"austin, plumbing",
		"Watch the block rate.",
		"Q3 campaigns",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("label editor missing %q", want)
		}
	}
}

func TestJobsPageShowsLabelsAndFiltersByFolder(t *testing.T) {
	t.Parallel()

	srv, repository := newMonitorSpecServer(t)
	repository.labels = JobLabels{
		JobID: monitorSpecJobID, Folder: "Q3 campaigns", Owner: "Dana", Tags: []string{"austin"},
	}

	recorder := httptest.NewRecorder()
	srv.jobsPage(recorder, httptest.NewRequest(http.MethodGet, "/app/jobs", http.NoBody))
	if recorder.Code != http.StatusOK {
		t.Fatalf("jobs status = %d", recorder.Code)
	}

	body := recorder.Body.String()
	for _, want := range []string{`data-job-labels`, ">austin<", ">Q3 campaigns<", `data-job-owner>Dana<`} {
		if !strings.Contains(body, want) {
			t.Fatalf("jobs page missing %q", want)
		}
	}
	if !strings.Contains(body, `id="job-folder"`) {
		t.Fatal("jobs page offers no folder filter although a folder is in use")
	}

	// A folder that no job carries must return an empty, clearly filtered view
	// rather than silently ignoring the parameter.
	recorder = httptest.NewRecorder()
	srv.jobsPage(recorder, httptest.NewRequest(http.MethodGet, "/app/jobs?folder=Nowhere", http.NoBody))
	if recorder.Code != http.StatusOK {
		t.Fatalf("filtered jobs status = %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "Austin plumbers") {
		t.Fatal("folder filter did not exclude a job from a different folder")
	}

	recorder = httptest.NewRecorder()
	srv.jobsPage(recorder, httptest.NewRequest(http.MethodGet, "/app/jobs?folder=Q3+campaigns", http.NoBody))
	if !strings.Contains(recorder.Body.String(), "Austin plumbers") {
		t.Fatal("folder filter excluded the job that is in that folder")
	}

	// Searching by tag must find the job, which is what the search field's
	// placeholder has always promised.
	recorder = httptest.NewRecorder()
	srv.jobsPage(recorder, httptest.NewRequest(http.MethodGet, "/app/jobs?q=austin", http.NoBody))
	if !strings.Contains(recorder.Body.String(), "Austin plumbers") {
		t.Fatal("tag search did not find the tagged job")
	}
}
