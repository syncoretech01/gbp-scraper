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

// TestMapHeatLayerTogglesServedWithAccessibleMarkup asserts that the Map
// Explorer page and the locally served script both carry the density heatmap
// toggle for results mode plus the failed-cell and empty-cell shading toggles
// for coverage mode, each as keyboard-reachable buttons with aria-pressed
// state and a text legend that explains what every colour means.
func TestMapHeatLayerTogglesServedWithAccessibleMarkup(t *testing.T) {
	t.Parallel()

	repository := newMapHandlerRepository()
	repository.job = Job{
		ID: "job-heat-ui", Name: "Heat layer UI", Date: time.Now().UTC(), Status: StatusOK,
		Data: JobData{Keywords: []string{"dentist"}},
	}
	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/app/map", nil)
	server.srv.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("map page status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`data-action="toggle-density-heat"`,
		`data-action="toggle-failed-heat"`,
		`data-action="toggle-empty-heat"`,
		`aria-pressed="false"`,
		"data-map-heat-legend",
		">Heatmap<",
		">Failed cells<",
		">Empty cells<",
		"darker blue means more results per bucket",
		"deeper red means more failed or blocked tasks",
		"amber marks completed cells with zero results",
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
		"toggle-density-heat",
		"toggle-failed-heat",
		"toggle-empty-heat",
		"renderDensityHeat",
		"setCoverageEmphasis",
		"aria-pressed",
		"densityHeatRamp",
		"failedHeatRamp",
		"heat-density-",
		"heat-empty-cell",
		"heat-muted-cell",
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
		".map-heat-legend",
		".heat-density-1", ".heat-density-4",
		".heat-failed-1", ".heat-failed-4",
		".heat-empty-cell",
		".heat-muted-cell",
		`.map-heat-toggle[aria-pressed="true"]`,
	} {
		if !strings.Contains(string(stylesheet), expected) {
			t.Fatalf("app.css missing %q", expected)
		}
	}
}

// TestPreviewMapCoverageCarriesPerCellHeatEvidence verifies the JSON contract
// the client heat layers rely on: the coverage payload already exposes
// result_count, failed_tasks, blocked_tasks, and the empty flag per cell, so
// the failed-cell and empty-cell shading need no extra requests.
func TestPreviewMapCoverageCarriesPerCellHeatEvidence(t *testing.T) {
	t.Parallel()

	geometry, err := ParseMapGeometry([]byte(`{"type":"Feature","properties":{"shape":"bbox","bbox":[-122.46,37.75,-122.38,37.83]},"geometry":null}`))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := PreviewMapGrid(geometry, 2, maximumMapGridCells)
	if err != nil || len(preview.Cells) < 2 {
		t.Fatalf("PreviewMapGrid() = %d cells, %v; want at least 2", len(preview.Cells), err)
	}

	repository := newMapHandlerRepository()
	repository.job = Job{ID: "job-heat", Name: "Heat evidence", Date: time.Now().UTC(), Status: StatusOK}
	repository.activities = []MapCellActivity{
		{
			SourceCell: preview.Cells[0].ID, TaskCount: 4, CompletedTasks: 1,
			FailedTasks: 2, BlockedTasks: 1, ResultCount: 4, RawResultCount: 6,
		},
		{
			SourceCell: preview.Cells[1].ID, TaskCount: 2, CompletedTasks: 2,
			ResultCount: 0, RawResultCount: 0,
		},
	}
	service := NewService(repository, t.TempDir())
	coverage, err := service.PreviewMapCoverage(context.Background(), repository.job.ID, geometry, 2)
	if err != nil {
		t.Fatalf("PreviewMapCoverage() error = %v", err)
	}

	failedCell := coverage.Cells[0]
	if failedCell.FailedTasks != 2 || failedCell.BlockedTasks != 1 || failedCell.ResultCount != 4 || failedCell.Empty {
		t.Fatalf("failed-evidence cell = %+v", failedCell)
	}
	emptyCell := coverage.Cells[1]
	if !emptyCell.Empty || emptyCell.State != "completed" || emptyCell.ResultCount != 0 {
		t.Fatalf("empty-evidence cell = %+v", emptyCell)
	}
	if coverage.Summary.EmptyCells != 1 || coverage.Summary.PartialCells != 1 {
		t.Fatalf("coverage summary = %+v", coverage.Summary)
	}

	raw, err := json.Marshal(coverage)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`"failed_tasks":2`,
		`"blocked_tasks":1`,
		`"result_count":4`,
		`"empty":true`,
	} {
		if !strings.Contains(string(raw), expected) {
			t.Fatalf("coverage payload missing %q in %s", expected, raw)
		}
	}
}
