package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

func TestParseMapGeometrySupportsPolygonMultiPolygonCircleAndBBox(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      string
		kind     string
		inside   MapPoint
		outside  MapPoint
		hole     *MapPoint
		boundary *MapPoint
	}{
		{
			name: "polygon with hole",
			raw:  `{"type":"Feature","properties":{"name":"Downtown"},"geometry":{"type":"Polygon","coordinates":[[[-122.50,37.70],[-122.30,37.70],[-122.30,37.90],[-122.50,37.90],[-122.50,37.70]],[[-122.43,37.76],[-122.39,37.76],[-122.39,37.80],[-122.43,37.80],[-122.43,37.76]]]}}`,
			kind: "polygon", inside: MapPoint{Latitude: 37.74, Longitude: -122.45},
			outside:  MapPoint{Latitude: 38, Longitude: -122.4},
			hole:     &MapPoint{Latitude: 37.78, Longitude: -122.41},
			boundary: &MapPoint{Latitude: 37.76, Longitude: -122.41},
		},
		{
			name: "multipolygon",
			raw:  `{"type":"MultiPolygon","coordinates":[[[[-122.50,37.70],[-122.45,37.70],[-122.45,37.75],[-122.50,37.75],[-122.50,37.70]]],[[[-122.35,37.80],[-122.30,37.80],[-122.30,37.85],[-122.35,37.85],[-122.35,37.80]]]]}`,
			kind: "multipolygon", inside: MapPoint{Latitude: 37.82, Longitude: -122.32},
			outside: MapPoint{Latitude: 37.77, Longitude: -122.40},
		},
		{
			name: "circle",
			raw:  `{"type":"Feature","properties":{"shape":"circle","radius_m":2000},"geometry":{"type":"Point","coordinates":[-122.4194,37.7749]}}`,
			kind: "circle", inside: MapPoint{Latitude: 37.78, Longitude: -122.42},
			outside: MapPoint{Latitude: 37.82, Longitude: -122.42},
		},
		{
			name: "bbox properties",
			raw:  `{"type":"Feature","properties":{"geometry_type":"bounding-box","min_lat":37.70,"min_lon":-122.50,"max_lat":37.80,"max_lon":-122.40},"geometry":null}`,
			kind: "bbox", inside: MapPoint{Latitude: 37.75, Longitude: -122.45},
			outside: MapPoint{Latitude: 37.85, Longitude: -122.45},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			geometry, err := ParseMapGeometry([]byte(test.raw))
			if err != nil {
				t.Fatalf("ParseMapGeometry() error = %v", err)
			}
			if geometry.Kind() != test.kind {
				t.Fatalf("Kind() = %q, want %q", geometry.Kind(), test.kind)
			}
			if !geometry.ContainsPoint(test.inside.Latitude, test.inside.Longitude) {
				t.Fatalf("inside point %+v was excluded", test.inside)
			}
			if geometry.ContainsPoint(test.outside.Latitude, test.outside.Longitude) {
				t.Fatalf("outside point %+v was included", test.outside)
			}
			if test.hole != nil && geometry.ContainsPoint(test.hole.Latitude, test.hole.Longitude) {
				t.Fatalf("hole point %+v was included", *test.hole)
			}
			if test.boundary != nil && !geometry.ContainsPoint(test.boundary.Latitude, test.boundary.Longitude) {
				t.Fatalf("hole boundary point %+v was excluded", *test.boundary)
			}
			canonical := geometry.GeoJSON()
			if !json.Valid(canonical) || !bytes.Contains(canonical, []byte(`"type":"Feature"`)) {
				t.Fatalf("canonical GeoJSON = %s", canonical)
			}
			reparsed, err := ParseMapGeometry(canonical)
			if err != nil || reparsed.Kind() != geometry.Kind() || !bytes.Equal(reparsed.GeoJSON(), canonical) {
				t.Fatalf("canonical round trip = %s, %+v, %v", reparsed.GeoJSON(), reparsed, err)
			}
		})
	}
}

func TestParseMapGeometryRejectsMalformedOrUnboundedInput(t *testing.T) {
	t.Parallel()

	tests := []string{
		``,
		`{"type":"FeatureCollection","features":[]}`,
		`{"type":"Feature","properties":{},"geometry":{"type":"Point","coordinates":[-122,37]}}`,
		`{"type":"Feature","properties":{"shape":"circle","radius_m":0},"geometry":{"type":"Point","coordinates":[-122,37]}}`,
		`{"type":"Feature","properties":{"shape":"circle","radius_m":600000},"geometry":{"type":"Point","coordinates":[-122,37]}}`,
		`{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,1]]]}`,
		`{"type":"Polygon","coordinates":[[[0,0],[1,1],[0,1],[1,0],[0,0]]]}`,
		`{"type":"Feature","properties":{"shape":"bbox","bbox":[1,1,0,0]},"geometry":null}`,
		`{"type":"Polygon","coordinates":[[[181,0],[182,0],[182,1],[181,1],[181,0]]]}`,
	}
	for _, raw := range tests {
		if geometry, err := ParseMapGeometry([]byte(raw)); !errors.Is(err, ErrInvalidMapGeometry) {
			t.Fatalf("ParseMapGeometry(%q) = %+v, %v; want ErrInvalidMapGeometry", raw, geometry, err)
		}
	}

	oversized := bytes.Repeat([]byte(" "), maximumMapGeoJSONBytes+1)
	if _, err := ParseMapGeometry(oversized); !errors.Is(err, ErrInvalidMapGeometry) {
		t.Fatalf("oversized error = %v", err)
	}
}

func TestPreviewMapGridIsDeterministicClippedAndBounded(t *testing.T) {
	t.Parallel()

	circle, err := ParseMapGeometry([]byte(`{"type":"Feature","properties":{"shape":"circle","radius_km":5},"geometry":{"type":"Point","coordinates":[-122.4194,37.7749]}}`))
	if err != nil {
		t.Fatal(err)
	}
	first, err := PreviewMapGrid(circle, 1.5, maximumMapGridCells)
	if err != nil {
		t.Fatalf("PreviewMapGrid() error = %v", err)
	}
	second, err := PreviewMapGrid(circle, 1.5, maximumMapGridCells)
	if err != nil {
		t.Fatalf("second PreviewMapGrid() error = %v", err)
	}
	if len(first.Cells) < 10 || !reflect.DeepEqual(first, second) {
		t.Fatalf("grid is empty or nondeterministic: first=%d second=%d", len(first.Cells), len(second.Cells))
	}
	seen := make(map[string]struct{}, len(first.Cells))
	for index, cell := range first.Cells {
		if cell.Number != index+1 || cell.State != "waiting" || !circle.ContainsPoint(cell.Centre.Latitude, cell.Centre.Longitude) {
			t.Fatalf("invalid clipped cell %+v", cell)
		}
		if _, duplicate := seen[cell.ID]; duplicate {
			t.Fatalf("duplicate deterministic cell ID %q", cell.ID)
		}
		seen[cell.ID] = struct{}{}
	}
	if _, err := PreviewMapGrid(circle, 1.5, 1); !errors.Is(err, ErrMapGridTooLarge) {
		t.Fatalf("bounded preview error = %v", err)
	}
	if _, err := PreviewMapGrid(circle, 0, maximumMapGridCells); !errors.Is(err, ErrInvalidMapGeometry) {
		t.Fatalf("invalid cell size error = %v", err)
	}

	tiny, err := ParseMapGeometry([]byte(`{"type":"Feature","properties":{"shape":"bbox","bbox":[-122.420,37.774,-122.419,37.775]},"geometry":null}`))
	if err != nil {
		t.Fatal(err)
	}
	tinyPreview, err := PreviewMapGrid(tiny, 10, maximumMapGridCells)
	if err != nil || len(tinyPreview.Cells) != 1 ||
		!tiny.ContainsPoint(tinyPreview.Cells[0].Centre.Latitude, tinyPreview.Cells[0].Centre.Longitude) {
		t.Fatalf("tiny preview = %+v, %v", tinyPreview, err)
	}
}

func TestPreviewMapGridIdentityIgnoresDisplayAndExclusionMetadata(t *testing.T) {
	t.Parallel()

	firstGeometry, err := ParseMapGeometry([]byte(`{"type":"Feature","properties":{"shape":"circle","radius_m":1500,"name":"Original"},"geometry":{"type":"Point","coordinates":[-122.4194,37.7749]}}`))
	if err != nil {
		t.Fatal(err)
	}
	secondGeometry, err := ParseMapGeometry([]byte(`{"type":"Feature","properties":{"shape":"circle","radius_m":1500,"name":"Renamed","excluded_cells":["cell-old"]},"geometry":{"type":"Point","coordinates":[-122.4194,37.7749]}}`))
	if err != nil {
		t.Fatal(err)
	}
	first, err := PreviewMapGrid(firstGeometry, 0.75, maximumMapGridCells)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PreviewMapGrid(secondGeometry, 0.75, maximumMapGridCells)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Cells) == 0 || len(first.Cells) != len(second.Cells) {
		t.Fatalf("cell counts = %d and %d", len(first.Cells), len(second.Cells))
	}
	for index := range first.Cells {
		if first.Cells[index].ID != second.Cells[index].ID {
			t.Fatalf("metadata changed cell %d ID from %q to %q", index, first.Cells[index].ID, second.Cells[index].ID)
		}
	}
}

func TestMapRoutesCRUDImportPreviewExportAndSpatialResults(t *testing.T) {
	t.Parallel()

	repository := newMapHandlerRepository()
	repository.searchPage = ResultPage{
		Results: []BusinessResult{{ID: "biz-one", Name: "Mapped Dental"}}, Total: 1, Limit: 25,
	}
	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	mux := http.NewServeMux()
	server.registerMapRoutes(mux)

	feature := `{"type":"Feature","properties":{"shape":"bbox","bbox":[-122.50,37.70,-122.40,37.80]},"geometry":null}`
	createBody := `{"id":"area-one","name":"San Francisco core","geojson":` + feature + `}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/maps/areas", strings.NewReader(createBody))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("create without CSRF status = %d", recorder.Code)
	}

	request = mapJSONRequest(http.MethodPost, "/api/v1/maps/areas", createBody, server.csrfToken)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated || !strings.Contains(recorder.Body.String(), `"id":"area-one"`) {
		t.Fatalf("create status = %d body = %s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/maps/areas?limit=10", nil)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "San Francisco core") {
		t.Fatalf("list status = %d body = %s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/maps/areas/area-one/export", nil)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "application/geo+json" || !json.Valid(bytes.TrimSpace(recorder.Body.Bytes())) {
		t.Fatalf("export status = %d type = %q body = %s", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
	}

	previewBody := `{"area_id":"area-one","cell_size_km":2.5}`
	request = mapJSONRequest(http.MethodPost, "/api/v1/maps/grid/preview", previewBody, "")
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"geometry_kind":"bbox"`) || !strings.Contains(recorder.Body.String(), `"cells"`) {
		t.Fatalf("preview status = %d body = %s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/maps/results?area_id=area-one&page_size=25&filter_field=city&filter_operator=eq&filter_value=San+Francisco", nil)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Mapped Dental") {
		t.Fatalf("results status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if len(repository.lastSearch.Filters) != 1 || repository.lastGeometry.Kind() != "bbox" {
		t.Fatalf("spatial search = %+v geometry = %s", repository.lastSearch, repository.lastGeometry.Kind())
	}

	updateBody := `{"name":"Renamed core"}`
	request = mapJSONRequest(http.MethodPut, "/api/v1/maps/areas/area-one", updateBody, server.csrfToken)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Renamed core") {
		t.Fatalf("update status = %d body = %s", recorder.Code, recorder.Body.String())
	}

	collection := `{"type":"FeatureCollection","features":[` +
		`{"type":"Feature","properties":{"name":"First imported","shape":"bbox","bbox":[-1,-1,0,0]},"geometry":null},` +
		`{"type":"Feature","properties":{"name":"Second imported","shape":"circle","radius_m":1000},"geometry":{"type":"Point","coordinates":[1,1]}}]}`
	request = mapJSONRequest(http.MethodPost, "/api/v1/maps/areas/import", collection, server.csrfToken)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated || !strings.Contains(recorder.Body.String(), "First imported") || !strings.Contains(recorder.Body.String(), "Second imported") {
		t.Fatalf("import status = %d body = %s", recorder.Code, recorder.Body.String())
	}

	request = mapJSONRequest(http.MethodDelete, "/api/v1/maps/areas/area-one", `{}`, server.csrfToken)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("delete status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/maps/areas/area-one", nil)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing area status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestMapTileRouteValidatesCoordinatesAndServesLocalCache(t *testing.T) {
	t.Parallel()

	dataFolder := t.TempDir()
	repository := newMapHandlerRepository()
	server, err := New(NewService(repository, dataFolder), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	cachePath := filepath.Join(dataFolder, "map-tiles", "3", "2", "4.png")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatal(err)
	}
	image := append([]byte{'\x89', 'P', 'N', 'G', '\r', '\n', '\x1a', '\n'}, []byte("cached-map-tile")...)
	if err := os.WriteFile(cachePath, image, 0o644); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/maps/tiles/3/2/4.png", nil)
	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "image/png" || !bytes.Equal(recorder.Body.Bytes(), image) {
		t.Fatalf("cached tile response = %d %q %q", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.Bytes())
	}
	if !strings.Contains(recorder.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("cache control = %q", recorder.Header().Get("Cache-Control"))
	}

	for _, target := range []string{
		"/api/v1/maps/tiles/20/0/0.png",
		"/api/v1/maps/tiles/3/8/0.png",
		"/api/v1/maps/tiles/3/0/-1.png",
		"/api/v1/maps/tiles/3/0/0.jpg",
	} {
		recorder = httptest.NewRecorder()
		server.srv.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid tile %q status = %d", target, recorder.Code)
		}
	}
}

func TestMapExplorerPageUsesLocalInteractiveAssetsAndSavedGeometry(t *testing.T) {
	t.Parallel()

	repository := newMapHandlerRepository()
	repository.job = Job{
		ID: "job-map-ui", Name: "San Francisco dentists", Date: time.Now().UTC(), Status: StatusOK,
		Data: JobData{Keywords: []string{"dentist", "dental clinic"}},
	}
	area, _, err := NormalizeSavedArea(SavedArea{
		ID: "sf-dentists", Name: "San Francisco dental area",
		GeoJSON:   []byte(`{"type":"Feature","properties":{"shape":"circle","radius_m":10000},"geometry":{"type":"Point","coordinates":[-122.4194,37.7749]}}`),
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	repository.areas[area.ID] = area
	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/app/map?area_id=sf-dentists&mode=results&q=dental", nil)
	server.srv.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("map page status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"/static/vendor/leaflet/leaflet.js",
		"/static/vendor/leaflet-draw/leaflet.draw.js",
		"/static/vendor/leaflet-markercluster/leaflet.markercluster.js",
		"data-tile-template=\"/api/v1/maps/tiles/{z}/{x}/{y}.png\"",
		"data-grid-endpoint=\"/api/v1/maps/grid/preview\"",
		"data-coverage-endpoint=\"/api/v1/maps/grid/coverage\"",
		"data-results-endpoint=\"/api/v1/maps/results\"",
		"data-results-export-endpoint=\"/api/v1/maps/results/export\"",
		"data-rescrape-endpoint=\"/api/v1/maps/cells/rescrape\"",
		"San Francisco dental area",
		"map-initial-geojson",
		"radius_m",
		"Draw polygon",
		"Restore excluded",
		"Paused",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("map page missing %q", expected)
		}
	}
	if strings.Contains(body, "cdnjs.cloudflare.com") || strings.Contains(body, "unpkg.com") {
		t.Fatal("Map Explorer page unexpectedly references a CDN")
	}

	javascript, err := fs.ReadFile(static, "static/js/app-map.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"L.markerClusterGroup", "excluded_cells", "filter_group", "draw-polygon", "areasEndpoint", "loadCoverage", "exportResults", "queueSelectedCells"} {
		if !strings.Contains(string(javascript), expected) {
			t.Fatalf("app-map.js missing %q", expected)
		}
	}
}

func TestSelectedJobMapUsesExactSavedAreaSnapshotBeforeBoundingBox(t *testing.T) {
	t.Parallel()

	repository := newMapHandlerRepository()
	repository.job = Job{
		ID: "job-exact-area", Name: "Exact polygon", Date: time.Now().UTC(), Status: StatusOK,
		Data: JobData{
			Keywords: []string{"dentist"}, GridCellKM: 1,
			GridBBox:    "37.70,-122.50,37.90,-122.30",
			AreaGeoJSON: `{"type":"Feature","properties":{"name":"Clipped polygon"},"geometry":{"type":"Polygon","coordinates":[[[-122.45,37.74],[-122.39,37.74],[-122.39,37.80],[-122.45,37.74]]]}}`,
		},
	}
	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	page, _, err := server.buildMapPage(httptest.NewRequest(http.MethodGet, "/app/map?job_id=job-exact-area", nil))
	if err != nil || page.GeometryType != "polygon" || !strings.Contains(page.AreaGeoJSON, "Clipped polygon") {
		t.Fatalf("exact-area map = %+v, %v", page, err)
	}
}

func TestMapGeometryReturnsStableExcludedCells(t *testing.T) {
	t.Parallel()

	geometry, err := ParseMapGeometry([]byte(`{
		"type":"Feature",
		"properties":{"shape":"bbox","bbox":[-122.45,37.75,-122.39,37.80],"excluded_cells":["cell-b","cell-a","cell-a","../bad"]},
		"geometry":{"type":"Polygon","coordinates":[[[-122.45,37.75],[-122.39,37.75],[-122.39,37.80],[-122.45,37.80],[-122.45,37.75]]]}
	}`))
	if err != nil {
		t.Fatalf("ParseMapGeometry: %v", err)
	}

	excluded := geometry.ExcludedCellIDs()
	if len(excluded) != 2 || excluded[0] != "cell-a" || excluded[1] != "cell-b" {
		t.Fatalf("ExcludedCellIDs() = %v", excluded)
	}
}

func TestSavedAreaFlowsIntoNewScrapeSnapshot(t *testing.T) {
	t.Parallel()

	repository := newMapHandlerRepository()
	now := time.Now().UTC()
	area := SavedArea{
		ID: "sf-dentists", Name: "San Francisco dental area",
		GeoJSON: json.RawMessage(`{
			"type":"Feature",
			"properties":{"shape":"circle","radius_m":7500,"excluded_cells":["cell-a"]},
			"geometry":{"type":"Point","coordinates":[-122.4194,37.7749]}
		}`),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateSavedArea(context.Background(), area); err != nil {
		t.Fatal(err)
	}
	server, err := New(NewService(repository, t.TempDir()), ":0")
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	server.newScrapePage(response, httptest.NewRequest(http.MethodGet, "/app/scrapes/new?area_id=sf-dentists", http.NoBody))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	for _, expected := range []string{"Saved area: San Francisco dental area", `name="saved_area_id" value="sf-dentists"`, `value="7500"`} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("wizard response missing %q", expected)
		}
	}

	form := validWizardForm(server.csrfToken)
	form.Set("saved_area_id", area.ID)
	form.Set("area_geojson", string(area.GeoJSON))
	request := httptest.NewRequest(http.MethodPost, "/app/scrapes", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := request.ParseForm(); err != nil {
		t.Fatal(err)
	}
	job, _, err := parseWizardJob(request)
	if err != nil {
		t.Fatalf("parseWizardJob: %v", err)
	}
	if job.Data.SavedAreaID != area.ID || job.Data.AreaGeoJSON == "" || job.Data.GridBBox == "" || job.Data.Radius != 7500 {
		t.Fatalf("saved area job data = %+v", job.Data)
	}
	geometry, err := ParseMapGeometry([]byte(job.Data.AreaGeoJSON))
	if err != nil {
		t.Fatalf("parse snapshotted area: %v", err)
	}
	if excluded := geometry.ExcludedCellIDs(); len(excluded) != 1 || excluded[0] != "cell-a" {
		t.Fatalf("snapshotted exclusions = %v", excluded)
	}
}

func TestPreviewMapCoverageProjectsDurableStatesAndHeatmapEvidence(t *testing.T) {
	t.Parallel()

	geometry, err := ParseMapGeometry([]byte(`{"type":"Feature","properties":{"shape":"bbox","bbox":[-122.42,37.77,-122.40,37.79]},"geometry":null}`))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := PreviewMapGrid(geometry, 2, maximumMapGridCells)
	if err != nil || len(preview.Cells) == 0 {
		t.Fatalf("PreviewMapGrid() = %+v, %v", preview, err)
	}
	repository := newMapHandlerRepository()
	repository.job = Job{ID: "job-coverage", Name: "Coverage", Date: time.Now().UTC(), Status: StatusOK}
	repository.activities = []MapCellActivity{{
		SourceCell: preview.Cells[0].ID, TaskCount: 2, CompletedTasks: 1, FailedTasks: 1,
		ResultCount: 3, RawResultCount: 5,
	}}
	service := NewService(repository, t.TempDir())
	coverage, err := service.PreviewMapCoverage(context.Background(), repository.job.ID, geometry, 2)
	if err != nil {
		t.Fatalf("PreviewMapCoverage() error = %v", err)
	}
	cell := coverage.Cells[0]
	if cell.State != "partial" || cell.ResultCount != 3 || cell.DuplicateCount != 2 ||
		coverage.Summary.PartialCells != 1 || coverage.Summary.DuplicateCount != 2 {
		t.Fatalf("coverage = %+v", coverage)
	}
	if got := mapCellCoverageState(MapCellActivity{TaskCount: 1, PendingTasks: 1}, jobruntime.StatePaused); got != "paused" {
		t.Fatalf("paused task state = %q", got)
	}
	if got := mapCellCoverageState(MapCellActivity{TaskCount: 1, RunningTasks: 1}, jobruntime.StatePaused); got != "running" {
		t.Fatalf("running task state = %q", got)
	}
}

func TestMapResultAreaExportAndSelectedCellKeywordJob(t *testing.T) {
	t.Parallel()

	repository := newMapHandlerRepository()
	repository.job = Job{
		ID: "source-job", Name: "Dentists", Date: time.Now().UTC(), Status: StatusOK,
		Data: JobData{
			Keywords: []string{"dentist"}, Lang: "en", Zoom: 14, Depth: 10,
			MaxTime: time.Hour, GridCellKM: 2,
		},
	}
	latitude, longitude, rating, reviews := 37.78, -122.41, 4.9, int64(10)
	repository.searchPage = ResultPage{Results: []BusinessResult{{
		ID: "business-one", Name: "=Formula Dental", PrimaryCategory: "Dentist",
		Latitude: &latitude, Longitude: &longitude, Rating: &rating, ReviewCount: &reviews,
	}}, Total: 1, Limit: 250}
	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	feature := `{"type":"Feature","properties":{"shape":"bbox","bbox":[-122.42,37.77,-122.40,37.79]},"geometry":null}`

	exportBody := `{"geojson":` + feature + `,"search":{"limit":250,"offset":0}}`
	request := mapJSONRequest(http.MethodPost, "/api/v1/maps/results/export?format=csv", exportBody, server.csrfToken)
	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Header().Get("Content-Type"), "text/csv") ||
		!strings.Contains(recorder.Body.String(), "'=Formula Dental") {
		t.Fatalf("area CSV export = %d %q %s", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
	}

	geometry, err := ParseMapGeometry([]byte(feature))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := PreviewMapGrid(geometry, 2, maximumMapGridCells)
	if err != nil || len(preview.Cells) == 0 {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	requestBody := fmt.Sprintf(`{"geojson":%s,"cell_size_km":2,"job_id":"source-job","cell_ids":[%q],"action":"keyword","keyword":"emergency dentist"}`, feature, preview.Cells[0].ID)
	request = mapJSONRequest(http.MethodPost, "/api/v1/maps/cells/rescrape", requestBody, server.csrfToken)
	recorder = httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated || repository.createdJob == nil || repository.createdState != jobruntime.StateQueued {
		t.Fatalf("selected-cell response = %d %s, job = %+v, state = %s", recorder.Code, recorder.Body.String(), repository.createdJob, repository.createdState)
	}
	if len(repository.createdJob.Data.Keywords) != 1 || repository.createdJob.Data.Keywords[0] != "emergency dentist" ||
		repository.createdJob.Data.AreaGeoJSON == "" {
		t.Fatalf("selected-cell job = %+v", repository.createdJob)
	}
	selectedGeometry, err := ParseMapGeometry([]byte(repository.createdJob.Data.AreaGeoJSON))
	if err != nil {
		t.Fatal(err)
	}
	if excluded := selectedGeometry.ExcludedCellIDs(); len(excluded) != len(preview.Cells)-1 {
		t.Fatalf("selected-cell exclusions = %d, want %d", len(excluded), len(preview.Cells)-1)
	}
}

func mapJSONRequest(method, target, body, csrf string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}

	return request
}

type mapHandlerRepository struct {
	*fixedJobRepository
	areas        map[string]SavedArea
	searchPage   ResultPage
	lastSearch   ResultSearch
	lastGeometry MapGeometry
	activities   []MapCellActivity
	createdJob   *Job
	createdState jobruntime.State
}

func newMapHandlerRepository() *mapHandlerRepository {
	return &mapHandlerRepository{
		fixedJobRepository: &fixedJobRepository{},
		areas:              make(map[string]SavedArea),
	}
}

func (repository *mapHandlerRepository) ListSavedAreas(_ context.Context, limit int) ([]SavedArea, error) {
	areas := make([]SavedArea, 0, len(repository.areas))
	for _, area := range repository.areas {
		areas = append(areas, area)
		if len(areas) == limit {
			break
		}
	}

	return areas, nil
}

func (repository *mapHandlerRepository) GetSavedArea(_ context.Context, id string) (SavedArea, error) {
	area, ok := repository.areas[id]
	if !ok {
		return SavedArea{}, ErrSavedAreaNotFound
	}

	return area, nil
}

func (repository *mapHandlerRepository) CreateSavedArea(_ context.Context, area SavedArea) error {
	if _, exists := repository.areas[area.ID]; exists {
		return ErrSavedAreaConflict
	}
	repository.areas[area.ID] = area

	return nil
}

func (repository *mapHandlerRepository) UpdateSavedArea(_ context.Context, area SavedArea) error {
	if _, exists := repository.areas[area.ID]; !exists {
		return ErrSavedAreaNotFound
	}
	repository.areas[area.ID] = area

	return nil
}

func (repository *mapHandlerRepository) DeleteSavedArea(_ context.Context, id string) error {
	if _, exists := repository.areas[id]; !exists {
		return ErrSavedAreaNotFound
	}
	delete(repository.areas, id)

	return nil
}

func (repository *mapHandlerRepository) SearchBusinessesInArea(
	_ context.Context,
	search ResultSearch,
	geometry MapGeometry,
) (ResultPage, error) {
	repository.lastSearch = search
	repository.lastGeometry = geometry

	return repository.searchPage, nil
}

func (repository *mapHandlerRepository) MapCellActivity(_ context.Context, _ string) ([]MapCellActivity, error) {
	return append([]MapCellActivity(nil), repository.activities...), nil
}

func (repository *mapHandlerRepository) CreateWithState(_ context.Context, job *Job, state jobruntime.State) error {
	copy := *job
	repository.createdJob = &copy
	repository.createdState = state

	return nil
}

var _ MapRepository = (*mapHandlerRepository)(nil)

func TestNormalizeSavedAreaBoundsMetadata(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0).UTC()
	area, geometry, err := NormalizeSavedArea(SavedArea{
		ID: "area-safe", Name: "  Safe area  ",
		GeoJSON:   []byte(`{"type":"Feature","properties":{"shape":"bbox","bbox":[-1,-1,1,1]},"geometry":null}`),
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil || area.Name != "Safe area" || geometry.Kind() != "bbox" {
		t.Fatalf("NormalizeSavedArea() = %+v, %s, %v", area, geometry.Kind(), err)
	}
	for _, invalid := range []SavedArea{
		{ID: "../escape", Name: "Unsafe", GeoJSON: area.GeoJSON},
		{ID: "safe", Name: strings.Repeat("x", 121), GeoJSON: area.GeoJSON},
		{ID: "safe", Name: "", GeoJSON: area.GeoJSON},
	} {
		if _, _, err := NormalizeSavedArea(invalid); !errors.Is(err, ErrInvalidMapGeometry) {
			t.Fatalf("NormalizeSavedArea(%+v) error = %v", invalid, err)
		}
	}
}
