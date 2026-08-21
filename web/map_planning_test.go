package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

const planningBBoxFeature = `{"type":"Feature","properties":{"shape":"bbox","bbox":[-122.44,37.75,-122.38,37.81]},"geometry":null}`

// Planning mode promises numbered cells. The numbering must be dense, 1-based,
// and in preview order, because the cell tooltip and every rescrape request
// identify a cell to the operator by that number.
func TestPlanningGridNumbersCellsDenselyFromOne(t *testing.T) {
	t.Parallel()

	geometry, err := ParseMapGeometry([]byte(planningBBoxFeature))
	if err != nil {
		t.Fatalf("ParseMapGeometry() error = %v", err)
	}
	preview, err := PreviewMapGrid(geometry, 2, maximumMapGridCells)
	if err != nil {
		t.Fatalf("PreviewMapGrid() error = %v", err)
	}
	if len(preview.Cells) < 4 {
		t.Fatalf("preview produced %d cells, want a multi-cell grid", len(preview.Cells))
	}

	seen := make(map[string]struct{}, len(preview.Cells))
	for index, cell := range preview.Cells {
		if cell.Number != index+1 {
			t.Fatalf("cell %d carries number %d, want %d", index, cell.Number, index+1)
		}
		if cell.ID == "" {
			t.Fatalf("cell %d has no identity", index)
		}
		if _, duplicate := seen[cell.ID]; duplicate {
			t.Fatalf("cell identity %q repeats", cell.ID)
		}
		seen[cell.ID] = struct{}{}
		if cell.State != "waiting" {
			t.Fatalf("planning cell %d state = %q, want waiting", cell.Number, cell.State)
		}
	}

	// Resizing the cell size is how an operator resizes cells: the same area
	// re-previews into a different, still dense, numbering.
	finer, err := PreviewMapGrid(geometry, 1, maximumMapGridCells)
	if err != nil {
		t.Fatalf("PreviewMapGrid(finer) error = %v", err)
	}
	if len(finer.Cells) <= len(preview.Cells) {
		t.Fatalf("halving the cell size produced %d cells, want more than %d",
			len(finer.Cells), len(preview.Cells))
	}
	if finer.Cells[len(finer.Cells)-1].Number != len(finer.Cells) {
		t.Fatal("the re-previewed grid is not densely numbered")
	}
}

// The planning estimate an operator launches against is queries times cells.
func TestMapPageReportsCellQueryAndTaskEstimate(t *testing.T) {
	t.Parallel()

	repository := newMapHandlerRepository()
	repository.job = Job{
		ID: "planning-job", Name: "Planning", Date: time.Now().UTC(), Status: StatusOK,
		Data: JobData{
			Keywords: []string{"dentist", "dental clinic", "orthodontist"},
			Lang:     "en", Zoom: 14, Depth: 10, MaxTime: time.Hour,
			AreaGeoJSON: planningBBoxFeature, GridCellKM: 2,
		},
	}
	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	page, _, err := server.buildMapPage(httptest.NewRequest(
		http.MethodGet, "/app/map?mode=planning&job_id=planning-job&grid_cell_km=2", http.NoBody,
	))
	if err != nil {
		t.Fatalf("buildMapPage() error = %v", err)
	}

	cells, queries, tasks := page.Estimate.Cells, page.Estimate.Queries, page.Estimate.Tasks
	if queries != "3" {
		t.Fatalf("query estimate = %q, want 3", queries)
	}
	if cells == "0" || cells == "" {
		t.Fatalf("cell estimate = %q, want a positive count", cells)
	}
	expected := fmt.Sprintf("%d", 3*atoiForTest(t, cells))
	if tasks != expected {
		t.Fatalf("task estimate = %q, want %q (queries x cells)", tasks, expected)
	}
}

// Live coverage colours are driven by the cell state the repository evidence
// resolves to. Green (completed) and red (failed/blocked) are the two the
// checklist singles out, and both must also reach the summary counters the
// rail renders.
func TestLiveCoverageResolvesCompletedAndFailedCellStates(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		activity MapCellActivity
		state    string
	}{
		{"completed", MapCellActivity{TaskCount: 2, CompletedTasks: 2, ResultCount: 9}, "completed"},
		{"failed", MapCellActivity{TaskCount: 2, FailedTasks: 2}, "failed"},
		{"blocked", MapCellActivity{TaskCount: 1, BlockedTasks: 1}, "blocked"},
		{"waiting", MapCellActivity{TaskCount: 2, PendingTasks: 2}, "waiting"},
	}
	for _, testCase := range cases {
		got := mapCellCoverageState(testCase.activity, jobruntime.StateRunning)
		if got != testCase.state {
			t.Errorf("%s cell state = %q, want %q", testCase.name, got, testCase.state)
		}
	}

	geometry, err := ParseMapGeometry([]byte(planningBBoxFeature))
	if err != nil {
		t.Fatalf("ParseMapGeometry() error = %v", err)
	}
	preview, err := PreviewMapGrid(geometry, 2, maximumMapGridCells)
	if err != nil || len(preview.Cells) < 3 {
		t.Fatalf("PreviewMapGrid() = %d cells, %v", len(preview.Cells), err)
	}

	repository := newMapHandlerRepository()
	repository.job = Job{ID: "coverage-colours", Name: "Coverage", Date: time.Now().UTC(), Status: StatusOK}
	repository.activities = []MapCellActivity{
		{SourceCell: preview.Cells[0].ID, TaskCount: 2, CompletedTasks: 2, ResultCount: 7, RawResultCount: 7},
		{SourceCell: preview.Cells[1].ID, TaskCount: 2, FailedTasks: 2},
		{SourceCell: preview.Cells[2].ID, TaskCount: 1, CompletedTasks: 1},
	}
	service := NewService(repository, t.TempDir())
	coverage, err := service.PreviewMapCoverage(context.Background(), repository.job.ID, geometry, 2)
	if err != nil {
		t.Fatalf("PreviewMapCoverage() error = %v", err)
	}
	if coverage.Cells[0].State != "completed" || coverage.Cells[1].State != "failed" {
		t.Fatalf("coverage states = %q, %q", coverage.Cells[0].State, coverage.Cells[1].State)
	}
	// A completed cell that returned nothing is the "empty cell" the results
	// mode offers to retry.
	if !coverage.Cells[2].Empty {
		t.Fatal("a completed cell with zero results was not marked empty")
	}
	// Empty counts every finished cell that produced nothing, so the failed
	// cell is empty too. The retry control accepts failed OR empty cells, so
	// the overlap is deliberate rather than double counting.
	if coverage.Summary.CompletedCells != 2 || coverage.Summary.FailedCells != 1 ||
		coverage.Summary.EmptyCells != 2 {
		t.Fatalf("coverage summary = %+v", coverage.Summary)
	}
}

// The explorer's results mode needs clustered markers, a populated business
// popup, and the retry/keyword controls. All three live in the vendored-asset
// template and app-map.js, so the assets and the DOM contract are asserted
// together rather than trusting either alone.
func TestMapExplorerShipsClusteringPopupsAndCellRescrapeControls(t *testing.T) {
	t.Parallel()

	markup, err := static.ReadFile("static/templates/app/pages/map.html")
	if err != nil {
		t.Fatalf("read map template: %v", err)
	}
	page := string(markup)
	for _, needle := range []string{
		"/static/vendor/leaflet-markercluster/leaflet.markercluster.js",
		"/static/vendor/leaflet-markercluster/MarkerCluster.css",
		`data-action="retry-cells"`,
		`data-action="keyword-cells"`,
		`data-action="remove-cells"`,
		`data-action="restore-cells"`,
		`data-map-cell-keyword`,
	} {
		if !strings.Contains(page, needle) {
			t.Errorf("map template is missing %q", needle)
		}
	}
	// The keyword-group control is behind a capability check, so its absence
	// is only correct when the whole block is conditional.
	if !strings.Contains(page, "data-map-keyword-group") || !strings.Contains(page, ".Page.KeywordGroups") {
		t.Error("the per-area keyword-group control is not gated on repository support")
	}

	script, err := static.ReadFile("static/js/app-map.js")
	if err != nil {
		t.Fatalf("read app-map.js: %v", err)
	}
	source := string(script)
	if !strings.Contains(source, "markerClusterGroup") {
		t.Error("result markers are not clustered")
	}
	// The popup must carry the fields the checklist names.
	for _, needle := range []string{
		`"Category"`, `"Address"`, `"Rating"`, `"Phone"`, `"Email"`,
		"primary_category", "review_count", "website_status", "maps_url",
	} {
		if !strings.Contains(source, needle) {
			t.Errorf("the business popup does not include %s", needle)
		}
	}
	// Vendored assets only: a CDN reference would break the strict CSP.
	for _, forbidden := range []string{"http://unpkg", "https://unpkg", "cdn.jsdelivr", "cdnjs.cloudflare"} {
		if strings.Contains(page, forbidden) || strings.Contains(source, forbidden) {
			t.Errorf("the map explorer references the remote host %q", forbidden)
		}
	}
}

func atoiForTest(t *testing.T, value string) int {
	t.Helper()

	var parsed int
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil {
		t.Fatalf("parse %q as a whole number: %v", value, err)
	}

	return parsed
}
