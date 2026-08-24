package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The Results table renders a large page through a scroll window: only the rows
// covering the viewport are in the document and two spacer rows carry the rest
// of the height. These tests pin the contract between the server and that
// window — the hooks the script binds to, the row numbering it needs to keep
// the grid honest for assistive technology, and the page sizes windowed
// rendering makes affordable.

// virtualisedResultsServer serves one Results page holding rows businesses
// starting at offset inside a result set of total records.
func virtualisedResultsServer(t *testing.T, rows int, offset int, total int64) *Server {
	t.Helper()

	template := coreColumnResultRow()
	results := make([]BusinessResult, 0, rows)

	for index := range rows {
		row := template
		row.ID = fmt.Sprintf("biz_%05d", offset+index)
		row.Name = fmt.Sprintf("Bay Smile Dental %d", offset+index)
		results = append(results, row)
	}

	repository := &fixedResultRepository{
		fixedJobRepository: &fixedJobRepository{job: Job{
			ID: "ba78441f-a048-4c9d-a8de-d0589e66f132", Name: "San Francisco dentists",
			Status: StatusOK, Date: template.UpdatedAt,
		}},
		page: ResultPage{
			Total: total, Limit: rows, Offset: offset, Results: results,
		},
		overview: ResultOverview{UniqueBusinesses: total, RawRecords: total},
	}
	repository.detail = BusinessDetail{Business: template}

	server, err := New(NewService(repository, t.TempDir()), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return server
}

func TestResultsPageCarriesTheRowVirtualisationHooks(t *testing.T) {
	t.Parallel()

	server := virtualisedResultsServer(t, 250, 0, 4000)
	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/app/results?page_size=250", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("results status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	body := recorder.Body.String()
	for _, hook := range []string{
		// The scroll container the window measures and listens on.
		"data-results-virtual-scroll",
		// The holder that keeps a windowed-out row's id in the bulk form.
		"data-selection-mirror",
		// The page's own position in the result set, so a windowed row can
		// still report its true aria-rowindex.
		`data-row-offset="0"`,
		// The grid reports the whole result set, not the rendered page.
		`aria-rowcount="4000"`,
		// The header is grid row 1; the script numbers the data rows from it.
		`<tr aria-rowindex="1">`,
	} {
		if !strings.Contains(body, hook) {
			t.Errorf("results page is missing the %q virtualisation hook", hook)
		}
	}
}

func TestResultsPageOffsetFollowsTheRequestedPage(t *testing.T) {
	t.Parallel()

	server := virtualisedResultsServer(t, 250, 500, 4000)
	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/app/results?page_size=250&page=3", nil))

	if body := recorder.Body.String(); !strings.Contains(body, `data-row-offset="500"`) {
		t.Fatal("results page did not carry the page offset the virtualised rows are numbered from")
	}
}

func TestResultsScriptWindowsTheRowsWithoutALibrary(t *testing.T) {
	t.Parallel()

	server := virtualisedResultsServer(t, 250, 0, 4000)
	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/static/js/app-results.js", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("results script status = %d", recorder.Code)
	}

	source := recorder.Body.String()
	for _, symbol := range []string{
		// The window itself: bounds, painting, and the safe degradation.
		"computeRowWindow", "rowIndexAtOffset", "paintRowWindow", "renderRows",
		"renderEveryRow", "virtualRowThreshold", "virtualOverscanRows", "virtualWindowLimit",
		// One measurement and one write per frame, never per row.
		"scheduleRowRender", "requestAnimationFrame", "measureRowHeights", "rebuildRowOffsets",
		// The state a row must keep across a trip out of the document.
		"syncSelectionMirror", "revealRow", "applyRowIndexes", "setFocusedCell",
		// Printing needs every row, not the window.
		`"beforeprint"`,
	} {
		if !strings.Contains(source, symbol) {
			t.Errorf("results script is missing the virtualisation symbol %q", symbol)
		}
	}

	// Virtualisation is a rendering strategy, not a dependency: nothing is
	// fetched and no table library is loaded.
	for _, forbidden := range []string{"tabulator", "cdn.", "import(", "requirejs"} {
		if strings.Contains(strings.ToLower(source), forbidden) {
			t.Errorf("results script reached for %q instead of windowing the rows itself", forbidden)
		}
	}
}

func TestResultsPageSizeGrowsWithoutMovingTheDefault(t *testing.T) {
	t.Parallel()

	// Windowed rendering makes a larger page affordable, so the shortcut offers
	// one — but the default page stays where it was.
	server := virtualisedResultsServer(t, 25, 0, 4000)
	recorder := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/app/results", nil))

	body := recorder.Body.String()
	if !strings.Contains(body, `<option value="500"`) {
		t.Error("results pagination did not offer the larger page size windowing allows")
	}

	if !strings.Contains(body, `<option value="25" selected`) {
		t.Error("results pagination moved the default page size away from 25")
	}

	search, err := parseResultSearch(httptest.NewRequest(http.MethodGet, "/app/results", nil))
	if err != nil {
		t.Fatalf("parseResultSearch() error = %v", err)
	}

	if search.Limit != defaultResultPageSize {
		t.Fatalf("default page size = %d, want %d", search.Limit, defaultResultPageSize)
	}

	capped, err := parseResultSearch(httptest.NewRequest(http.MethodGet, "/app/results?page_size=100000", nil))
	if err != nil {
		t.Fatalf("parseResultSearch() error = %v", err)
	}

	if capped.Limit != maximumResultPageSize {
		t.Fatalf("capped page size = %d, want %d", capped.Limit, maximumResultPageSize)
	}
}
