package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

const geographyMonitorJobID = "66666666-6666-6666-6666-666666666666"

// newGeographyMonitorServer renders the monitor for a job shaped like the
// acceptance run: a radius, a grid cut from the square around it, and results
// spread from inside the circle to well past anything the plan pointed at.
func newGeographyMonitorServer(t *testing.T) *Server {
	t.Helper()

	dir := t.TempDir()
	writeCSV(t, dir, geographyMonitorJobID, strings.Join([]string{
		"place_id,title,latitude,longitude,website,phone,emails",
		"p1,Downtown Ink,34.0500,-118.2500,,,",
		"p2,Echo Park Tattoo,34.0780,-118.2600,,,",
		"p3,Corner Cell Studio,34.1860,-118.0820,,,",
		"p4,Anaheim Ink,33.84450,-117.94134,,,",
	}, "\n"))

	repository := &monitorSpecRepository{version: "v1.17.3"}
	repository.job = Job{
		ID:     geographyMonitorJobID,
		Name:   "Tattoo artists",
		Date:   time.Now().UTC(),
		Status: StatusOK,
		Data: JobData{
			Keywords:   []string{"tattoo artist"},
			Lat:        "34.0522",
			Lon:        "-118.2437",
			Radius:     15000,
			GridBBox:   "33.917302,-118.406517,34.187098,-118.080883",
			GridCellKM: 5,
			MaxTime:    90 * time.Minute,
		},
	}
	repository.runtime = JobRuntime{
		JobID:      geographyMonitorJobID,
		State:      jobruntime.StateCompleted,
		Stage:      jobruntime.StageSavingExporting,
		TotalTasks: 180, Completed: 180,
		RawRecords: 4, UniqueRecords: 4,
	}

	srv, err := New(NewService(repository, dir), ":0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return srv
}

func renderGeographyMonitor(t *testing.T, srv *Server) string {
	t.Helper()

	request := requestWithID(httptest.NewRequest(
		http.MethodGet,
		"/app/jobs/"+geographyMonitorJobID+"?id="+geographyMonitorJobID,
		http.NoBody,
	))
	recorder := httptest.NewRecorder()
	srv.jobMonitorPage(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("monitor status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	return recorder.Body.String()
}

// TestJobMonitorRendersWhereTheResultsLanded is the regression for issue O's
// presentation half: a run that kept businesses outside its radius must say so
// on the page, and must offer a way to narrow the view without destroying
// anything.
func TestJobMonitorRendersWhereTheResultsLanded(t *testing.T) {
	t.Parallel()

	body := renderGeographyMonitor(t, newGeographyMonitorServer(t))

	for _, want := range []string{
		"Where the results landed",
		"Inside the 15.0 km radius",
		"Past the radius, inside the searched grid",
		"Outside the area this run searched",
		"Farthest business kept",
		"Anaheim Ink",
		"Show only the businesses inside 15.0 km",
		"filter_field=distance",
		"comes back when the filter is cleared",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("monitor page is missing %q", want)
		}
	}

	// Two of four are inside the 15 km circle, one is in the grid's corner
	// band, one is Maps spillover from Anaheim.
	if !strings.Contains(body, "2 of 4") {
		t.Error("the inside-radius count is not rendered as 2 of 4")
	}
}

// TestJobMonitorNeverCallsRepetitionAMerge is the regression for issue P's
// presentation half. The tile that read "Duplicates merged 0 — the same
// business seen more than once" was wrong twice over: the CSV it counted has
// already had its repeats folded away, so it was structurally zero, and
// nothing it counted was ever an entity merge.
func TestJobMonitorNeverCallsRepetitionAMerge(t *testing.T) {
	t.Parallel()

	body := renderGeographyMonitor(t, newGeographyMonitorServer(t))

	for _, forbidden := range []string{
		"Duplicates merged",
		"the same business seen more than once",
		">Rows collected<",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("monitor page still shows %q", forbidden)
		}
	}

	for _, want := range []string{
		"Businesses kept",
		"Maps observations",
		"Repeated observations",
		"data-run-observations",
		"data-run-repeats",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("monitor page is missing %q", want)
		}
	}

	// The observation tiles must ship hidden: the business count is the only
	// figure the server can prove without the per-task checkpoints.
	if !strings.Contains(body, "data-run-observations hidden") {
		t.Error("the Maps observations tile is not hidden before its evidence arrives")
	}
	if !strings.Contains(body, "data-run-repeats hidden") {
		t.Error("the repeated observations tile is not hidden before its evidence arrives")
	}
}

// A job with no centre has no distance to report, and the panel must be absent
// rather than framing an empty grid.
func TestJobMonitorOmitsGeographyWithoutACentre(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeCSV(t, dir, geographyMonitorJobID,
		"place_id,title,latitude,longitude\np1,Somewhere,34.05,-118.25\n")

	repository := &monitorSpecRepository{}
	repository.job = Job{
		ID: geographyMonitorJobID, Name: "No centre", Date: time.Now().UTC(),
		Status: StatusOK, Data: JobData{Keywords: []string{"tattoo artist"}},
	}
	repository.runtime = JobRuntime{JobID: geographyMonitorJobID, State: jobruntime.StateCompleted}

	srv, err := New(NewService(repository, dir), ":0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body := renderGeographyMonitor(t, srv)
	if strings.Contains(body, "Where the results landed") {
		t.Error("the geography panel rendered for a job with no configured centre")
	}
}
