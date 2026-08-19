package web

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mapDuplicatesRepository is a self-contained fixture for the duplicate-density
// coverage layer. It returns canned per-cell activity aggregates plus the
// durable tasks whose checkpoints record DuplicatesSkipped and RowsReplaced.
type mapDuplicatesRepository struct {
	*fixedJobRepository
	activities []MapCellActivity
	tasks      []JobTask
}

func newMapDuplicatesRepository() *mapDuplicatesRepository {
	return &mapDuplicatesRepository{fixedJobRepository: &fixedJobRepository{}}
}

func (repository *mapDuplicatesRepository) MapCellActivity(context.Context, string) ([]MapCellActivity, error) {
	return append([]MapCellActivity(nil), repository.activities...), nil
}

func (repository *mapDuplicatesRepository) MapCellTasks(context.Context, string) ([]JobTask, error) {
	return append([]JobTask(nil), repository.tasks...), nil
}

// TestPreviewMapCoverageSumsCheckpointDuplicatesPerCell proves that the
// coverage payload carries a per-cell "duplicates" count built from the same
// durable job-task evidence as the rest of the cell: the DuplicatesSkipped and
// RowsReplaced totals of every checkpoint committed in that cell.
func TestPreviewMapCoverageSumsCheckpointDuplicatesPerCell(t *testing.T) {
	t.Parallel()

	geometry, err := ParseMapGeometry([]byte(`{"type":"Feature","properties":{"shape":"bbox","bbox":[-122.46,37.75,-122.38,37.83]},"geometry":null}`))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := PreviewMapGrid(geometry, 2, maximumMapGridCells)
	if err != nil || len(preview.Cells) < 2 {
		t.Fatalf("PreviewMapGrid() = %d cells, %v; want at least 2", len(preview.Cells), err)
	}
	duplicateCell := preview.Cells[0].ID
	cleanCell := preview.Cells[1].ID

	repository := newMapDuplicatesRepository()
	repository.job = Job{ID: "job-duplicate-density", Name: "Duplicate density", Date: time.Now().UTC(), Status: StatusOK}
	repository.activities = []MapCellActivity{
		{SourceCell: duplicateCell, TaskCount: 2, CompletedTasks: 2, ResultCount: 6, RawResultCount: 6},
		{SourceCell: cleanCell, TaskCount: 1, CompletedTasks: 1, ResultCount: 2, RawResultCount: 2},
	}
	// Two finished tasks in the same cell: their checkpoint counts must be
	// summed (3+1 and 2+4 makes 10). The remaining tasks prove tolerance for
	// checkpoints without duplicate evidence and for unparseable payloads.
	repository.tasks = []JobTask{
		{JobID: repository.job.ID, Key: "cell-a-task-1", SourceCell: duplicateCell, State: "completed",
			Checkpoint: json.RawMessage(`{"state":"completed","rows_added":6,"rows_replaced":1,"duplicates_skipped":3}`)},
		{JobID: repository.job.ID, Key: "cell-a-task-2", SourceCell: duplicateCell, State: "completed",
			Checkpoint: json.RawMessage(`{"state":"completed","rows_added":2,"rows_replaced":4,"duplicates_skipped":2}`)},
		{JobID: repository.job.ID, Key: "cell-b-task-1", SourceCell: cleanCell, State: "completed",
			Checkpoint: json.RawMessage(`{"state":"completed","rows_added":2}`)},
		{JobID: repository.job.ID, Key: "broken-checkpoint", SourceCell: cleanCell, State: "completed",
			Checkpoint: json.RawMessage(`not-json`)},
	}

	service := NewService(repository, t.TempDir())
	coverage, err := service.PreviewMapCoverage(context.Background(), repository.job.ID, geometry, 2)
	if err != nil {
		t.Fatalf("PreviewMapCoverage() error = %v", err)
	}
	byID := make(map[string]MapGridCell, len(coverage.Cells))
	for _, cell := range coverage.Cells {
		byID[cell.ID] = cell
	}
	if got := byID[duplicateCell].Duplicates; got != 10 {
		t.Fatalf("duplicate cell duplicates = %d, want 10 (3+1 plus 2+4)", got)
	}
	if got := byID[cleanCell].Duplicates; got != 0 {
		t.Fatalf("clean cell duplicates = %d, want 0", got)
	}

	payload, err := json.Marshal(coverage)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"duplicates":10`) {
		t.Fatalf("coverage payload missing per-cell duplicates field: %s", payload)
	}

	// The HTTP coverage endpoint must serve the same additive field.
	server, err := New(service, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	body := `{"geojson":{"type":"Feature","properties":{"shape":"bbox","bbox":[-122.46,37.75,-122.38,37.83]},"geometry":null},"cell_size_km":2,"job_id":"job-duplicate-density"}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/maps/grid/coverage", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"duplicates":10`) {
		t.Fatalf("coverage endpoint = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

// TestMapCellCheckpointDuplicatesIgnoresUnusableTasks pins the aggregation
// rules: blank source cells, empty checkpoints, invalid JSON, and negative
// counts all contribute nothing.
func TestMapCellCheckpointDuplicatesIgnoresUnusableTasks(t *testing.T) {
	t.Parallel()

	totals := mapCellCheckpointDuplicates([]JobTask{
		{SourceCell: "  ", Checkpoint: json.RawMessage(`{"duplicates_skipped":5}`)},
		{SourceCell: "cell-x"},
		{SourceCell: "cell-x", Checkpoint: json.RawMessage(`{`)},
		{SourceCell: "cell-x", Checkpoint: json.RawMessage(`{"duplicates_skipped":-3,"rows_replaced":-2}`)},
		{SourceCell: "cell-x", Checkpoint: json.RawMessage(`{"duplicates_skipped":2,"rows_replaced":-1}`)},
		{SourceCell: " cell-x ", Checkpoint: json.RawMessage(`{"rows_replaced":4}`)},
	})
	total := totals["cell-x"]
	if total.DuplicatesSkipped != 2 || total.RowsReplaced != 4 {
		t.Fatalf("cell-x checkpoint totals = %+v, want 2 skipped and 4 replaced", total)
	}
	if activity := (MapCellActivity{DuplicatesSkipped: 2, RowsReplaced: 4}); activity.CheckpointDuplicates() != 6 {
		t.Fatalf("CheckpointDuplicates() = %d, want 6", activity.CheckpointDuplicates())
	}
	if len(totals) != 1 {
		t.Fatalf("checkpoint totals = %+v, want only cell-x", totals)
	}
}

// TestMapDuplicateHeatToggleServedWithTextLegend asserts that the served Map
// Explorer page and the locally bundled script/stylesheet carry the fourth
// heat toggle: a keyboard-reachable "Duplicate-heavy cells" button with
// aria-pressed state, a 4-step purple ramp, and text that never relies on
// colour alone.
func TestMapDuplicateHeatToggleServedWithTextLegend(t *testing.T) {
	t.Parallel()

	repository := newMapDuplicatesRepository()
	repository.job = Job{
		ID: "job-duplicate-heat-ui", Name: "Duplicate heat UI", Date: time.Now().UTC(), Status: StatusOK,
		Data: JobData{Keywords: []string{"dentist"}},
	}
	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/app/map", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("map page status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`data-action="toggle-duplicate-heat"`,
		">Duplicate-heavy cells<",
		`aria-pressed="false"`,
		"deeper purple means more duplicate rows",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("map page missing %q", expected)
		}
	}

	javascript, err := fs.ReadFile(static, "static/js/app-map.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"toggle-duplicate-heat",
		"duplicateHeatRamp",
		"cellDuplicateCount",
		"maximumCellDuplicates",
		"heat-duplicate-",
		"no recorded duplicates (muted)",
		"no cell has recorded duplicate rows yet",
	} {
		if !strings.Contains(string(javascript), expected) {
			t.Fatalf("app-map.js missing %q", expected)
		}
	}

	stylesheet, err := fs.ReadFile(static, "static/css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		".heat-duplicate-1", ".heat-duplicate-2", ".heat-duplicate-3", ".heat-duplicate-4",
	} {
		if !strings.Contains(string(stylesheet), expected) {
			t.Fatalf("app.css missing %q", expected)
		}
	}
}
