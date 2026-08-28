package web

import (
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web/enrichment"
	"github.com/gosom/google-maps-scraper/web/resultimport"
)

// enrichmentPipelineRepositoryStub adds the optional post-enrichment readbacks
// to the shared queue stub.
type enrichmentPipelineRepositoryStub struct {
	*enrichmentRepositoryStub

	mutex     sync.Mutex
	contacts  []JobBusinessContacts
	pending   int
	refreshed []string
	reuse     *DomainAuditReuse
	reuseFor  string
}

func (repository *enrichmentPipelineRepositoryStub) RefreshJobEnrichmentTotals(
	_ context.Context,
	jobID string,
) (JobEnrichmentTotals, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()

	repository.refreshed = append(repository.refreshed, jobID)

	return JobEnrichmentTotals{}, nil
}

func (repository *enrichmentPipelineRepositoryStub) PendingEnrichmentTaskCount(
	_ context.Context,
	_ string,
) (int, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()

	return repository.pending, nil
}

func (repository *enrichmentPipelineRepositoryStub) JobBusinessContacts(
	_ context.Context,
	_ string,
) ([]JobBusinessContacts, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()

	return repository.contacts, nil
}

func (repository *enrichmentPipelineRepositoryStub) ReusableDomainAudit(
	_ context.Context,
	websiteURL string,
	_ time.Time,
	_ int,
) (DomainAuditReuse, bool, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()

	if repository.reuse == nil || repository.reuseFor != websiteURL {
		return DomainAuditReuse{}, false, nil
	}

	return *repository.reuse, true, nil
}

func (repository *enrichmentPipelineRepositoryStub) refreshedJobs() []string {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()

	return append([]string(nil), repository.refreshed...)
}

// writeLegacyJobCSV writes one job result file with the legacy header and
// column order, and returns its path.
func writeLegacyJobCSV(t *testing.T, folder, jobID string, values map[string]string) string {
	t.Helper()

	path := filepath.Join(folder, jobID+".csv")

	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create result file: %v", err)
	}

	writer := csv.NewWriter(file)
	headers := resultimport.LegacyHeaders()

	if err := writer.Write(headers); err != nil {
		t.Fatalf("write header: %v", err)
	}

	row := make([]string, len(headers))
	for index, header := range headers {
		row[index] = values[header]
	}

	if err := writer.Write(row); err != nil {
		t.Fatalf("write row: %v", err)
	}

	writer.Flush()

	if err := writer.Error(); err != nil {
		t.Fatalf("flush result file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close result file: %v", err)
	}

	return path
}

func readResultFile(t *testing.T, path string) ([]string, [][]string) {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open result file: %v", err)
	}

	defer func() { _ = file.Close() }()

	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1

	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("read result file: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("result file has no header")
	}

	return records[0], records[1:]
}

// TestEnrichmentPassExportsTheEmailsItHolds is the regression for the
// acceptance defect: job cfe2d653 stored eleven businesses with real addresses
// while its headline and its export both reported zero, because the result file
// is written during the scrape and was never rewritten afterwards.
func TestEnrichmentPassExportsTheEmailsItHolds(t *testing.T) {
	t.Parallel()

	folder := t.TempDir()
	jobID := "job-export-emails"
	path := writeLegacyJobCSV(t, folder, jobID, map[string]string{
		"place_id": "place-1",
		"title":    "Neptune Tattoo Studio",
		"address":  "1 Market St",
		"website":  "https://neptunetattoostudio.com",
		"phone":    "+1 626 555 0100",
		"emails":   "",
	})

	base := &enrichmentRepositoryStub{pending: []EnrichmentTask{{
		ID: "task-1", BusinessID: "business-1", JobID: jobID,
		WebsiteURL: "https://neptunetattoostudio.com", State: "queued",
		Options: EnrichmentOptions{
			Scope: string(enrichment.ScopeHomepage), MaxPages: 1, TimeoutSeconds: 5,
			MaxBodyBytes: 2048, MaxRedirects: 2, DisableInternalChecks: true,
		},
	}}}
	repository := &enrichmentPipelineRepositoryStub{
		enrichmentRepositoryStub: base,
		contacts: []JobBusinessContacts{{
			BusinessID: "business-1", PlaceID: "place-1",
			Name: "Neptune Tattoo Studio", Address: "1 Market St",
			Emails: []string{"inquiries@neptunetattoostudio.com"},
		}},
	}

	service := NewService(repository, folder)
	factory := func(EnrichmentOptions) (websiteAnalyzer, error) {
		return analyzerStub{result: enrichment.Result{
			RequestedURL: "https://neptunetattoostudio.com",
			FinalURL:     "https://neptunetattoostudio.com",
			Reachable:    true, StatusCode: 200,
		}}, nil
	}

	processed, err := service.processEnrichmentQueue(context.Background(), 4, factory)
	if err != nil || processed != 1 {
		t.Fatalf("processEnrichmentQueue() = %d, %v", processed, err)
	}

	if jobs := repository.refreshedJobs(); len(jobs) != 1 || jobs[0] != jobID {
		t.Fatalf("refreshed jobs = %v, want [%s]", jobs, jobID)
	}

	header, rows := readResultFile(t, path)
	if len(header) != len(resultimport.LegacyHeaders()) {
		t.Fatalf("result header has %d columns, want %d", len(header), len(resultimport.LegacyHeaders()))
	}

	for index, name := range resultimport.LegacyHeaders() {
		if header[index] != name {
			t.Fatalf("result header column %d = %q, want %q", index, header[index], name)
		}
	}

	if len(rows) != 1 {
		t.Fatalf("result rows = %d, want 1", len(rows))
	}

	emailColumn := len(header) - 1
	if header[emailColumn] != "emails" {
		t.Fatalf("last column = %q, want emails", header[emailColumn])
	}
	if rows[0][emailColumn] != "inquiries@neptunetattoostudio.com" {
		t.Fatalf("exported emails = %q, want the address the workspace holds", rows[0][emailColumn])
	}
	// Every other cell must be byte-identical to what the scrape wrote.
	if rows[0][2] != "Neptune Tattoo Studio" || rows[0][7] != "https://neptunetattoostudio.com" {
		t.Fatalf("the rewrite changed an unrelated column: %#v", rows[0])
	}
}

// TestEnrichmentPassKeepsTheResultFileUntilItsQueueDrains proves the export is
// only rewritten once the job's website audits have all reached a terminal
// state, so a partial total is never presented as final.
func TestEnrichmentPassKeepsTheResultFileUntilItsQueueDrains(t *testing.T) {
	t.Parallel()

	folder := t.TempDir()
	jobID := "job-still-running"
	path := writeLegacyJobCSV(t, folder, jobID, map[string]string{
		"place_id": "place-1", "title": "Pending", "emails": "",
	})

	base := &enrichmentRepositoryStub{pending: []EnrichmentTask{{
		ID: "task-1", BusinessID: "business-1", JobID: jobID,
		WebsiteURL: "https://pending.example", State: "queued",
	}}}
	repository := &enrichmentPipelineRepositoryStub{
		enrichmentRepositoryStub: base,
		pending:                  3,
		contacts: []JobBusinessContacts{{
			BusinessID: "business-1", PlaceID: "place-1",
			Emails: []string{"hello@pending.example"},
		}},
	}

	service := NewService(repository, folder)
	factory := func(EnrichmentOptions) (websiteAnalyzer, error) {
		return analyzerStub{result: enrichment.Result{Reachable: true}}, nil
	}

	if _, err := service.processEnrichmentQueue(context.Background(), 1, factory); err != nil {
		t.Fatalf("processEnrichmentQueue() error = %v", err)
	}

	if jobs := repository.refreshedJobs(); len(jobs) != 0 {
		t.Fatalf("totals were refreshed while %d audits were still queued: %v", repository.pending, jobs)
	}

	_, rows := readResultFile(t, path)
	if rows[0][len(rows[0])-1] != "" {
		t.Fatalf("the export was rewritten before the queue drained: %q", rows[0][len(rows[0])-1])
	}
}

// TestEnrichmentPassReusesAFreshAuditOfTheSamePage proves the domain cache: a
// second business pointing at a page the workspace audited recently is served
// from that evidence instead of crawling the site again.
func TestEnrichmentPassReusesAFreshAuditOfTheSamePage(t *testing.T) {
	t.Parallel()

	observed := time.Now().UTC().Add(-time.Hour)
	base := &enrichmentRepositoryStub{pending: []EnrichmentTask{{
		ID: "task-reuse", BusinessID: "business-2", JobID: "job-reuse",
		WebsiteURL: "https://www.instagram.com/esto_lts", State: "queued",
		Options: EnrichmentOptions{StaleAfterHours: 24},
	}}}
	repository := &enrichmentPipelineRepositoryStub{
		enrichmentRepositoryStub: base,
		pending:                  1,
		reuseFor:                 "https://www.instagram.com/esto_lts",
		reuse: &DomainAuditReuse{
			AuditID: 7, Domain: "instagram.com", CompletedAt: observed,
			Result: enrichment.Result{
				RequestedURL: "https://instagram.com/esto_lts",
				FinalURL:     "https://www.instagram.com/esto_lts",
				Reachable:    true, StatusCode: 200,
				Emails: []enrichment.Email{{Address: "shop@esto.example"}},
			},
		},
	}

	var crawls atomic.Int64

	service := NewService(repository, t.TempDir())
	factory := func(EnrichmentOptions) (websiteAnalyzer, error) {
		crawls.Add(1)

		return analyzerStub{result: enrichment.Result{Reachable: true}}, nil
	}

	processed, err := service.processEnrichmentQueue(context.Background(), 1, factory)
	if err != nil || processed != 1 {
		t.Fatalf("processEnrichmentQueue() = %d, %v", processed, err)
	}

	if crawls.Load() != 0 {
		t.Fatalf("the site was crawled %d times despite fresh evidence for the same page", crawls.Load())
	}
	if base.stored != "task-reuse" || base.finished != "task-reuse" {
		t.Fatalf("a reused audit was not stored and finished: %+v", base)
	}
}

// TestEnrichmentPassRunsAuditsConcurrently proves the second pipeline stage is
// a bounded pool rather than the single task per worker tick that made the
// acceptance run take 157 seconds for 25 sites.
func TestEnrichmentPassRunsAuditsConcurrently(t *testing.T) {
	// Not parallel: it replaces the process-wide pool capacity, which every
	// other pass in this package reads.
	tasks := make([]EnrichmentTask, 0, 6)
	for index := range 6 {
		tasks = append(tasks, EnrichmentTask{
			ID:         "task-" + string(rune('a'+index)),
			BusinessID: "business-" + string(rune('a'+index)),
			JobID:      "job-pool",
			WebsiteURL: "https://host" + string(rune('a'+index)) + ".example",
			State:      "queued",
		})
	}

	repository := &enrichmentPipelineRepositoryStub{
		enrichmentRepositoryStub: &enrichmentRepositoryStub{pending: tasks},
		pending:                  1,
	}

	SetEnrichmentPool(EnrichmentPoolConfig{Workers: 4, MaxConcurrentPerHost: 1})

	t.Cleanup(func() {
		SetEnrichmentPool(EnrichmentPoolConfig{
			Workers:              defaultEnrichmentWorkers,
			MaxConcurrentPerHost: defaultEnrichmentHostConcurrency,
			MinHostInterval:      defaultEnrichmentHostInterval,
		})
	})

	var (
		mutex   sync.Mutex
		current int
		peak    int
	)

	admitted := make(chan struct{}, len(tasks))
	release := make(chan struct{})

	service := NewService(repository, t.TempDir())
	factory := func(EnrichmentOptions) (websiteAnalyzer, error) {
		return blockingAnalyzer{
			onStart: func() {
				mutex.Lock()
				current++
				if current > peak {
					peak = current
				}
				mutex.Unlock()

				admitted <- struct{}{}
				<-release

				mutex.Lock()
				current--
				mutex.Unlock()
			},
		}, nil
	}

	done := make(chan error, 1)

	go func() {
		_, err := service.processEnrichmentQueue(context.Background(), len(tasks), factory)
		done <- err
	}()

	for range 2 {
		select {
		case <-admitted:
		case <-time.After(10 * time.Second):
			t.Fatal("the enrichment pass ran audits one at a time")
		}
	}

	close(release)

	if err := <-done; err != nil {
		t.Fatalf("processEnrichmentQueue() error = %v", err)
	}

	mutex.Lock()
	defer mutex.Unlock()

	if peak < 2 {
		t.Fatalf("peak concurrent audits = %d, want at least 2", peak)
	}
	if peak > 4 {
		t.Fatalf("peak concurrent audits = %d, want no more than the configured 4", peak)
	}
}

type blockingAnalyzer struct {
	onStart func()
}

func (analyzer blockingAnalyzer) Analyze(_ context.Context, rawURL string) (enrichment.Result, error) {
	analyzer.onStart()

	return enrichment.Result{RequestedURL: rawURL, Reachable: true}, nil
}

func TestSplitCSVEmailsReadsBothLegacyFormats(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		in   string
		want []string
	}{
		{in: "a@example.com, b@example.com", want: []string{"a@example.com", "b@example.com"}},
		{in: "[a@example.com b@example.com]", want: []string{"a@example.com", "b@example.com"}},
		{in: "", want: []string{}},
	} {
		got := splitCSVEmails(testCase.in)
		if strings.Join(got, "|") != strings.Join(testCase.want, "|") {
			t.Fatalf("splitCSVEmails(%q) = %v, want %v", testCase.in, got, testCase.want)
		}
	}
}

// TestEnrichmentPassStartsOneBrowserForTheWholePass guards the resource
// boundary the pool must not cross. Website audits run several at a time, but
// screenshot capture needs a browser, and one browser per worker would turn a
// throughput improvement into a memory problem on a local machine.
func TestEnrichmentPassStartsOneBrowserForTheWholePass(t *testing.T) {
	// Not parallel: it replaces the browser seam and the pool capacity.
	var created atomic.Int64

	capturer := &fakeScreenshotCapturer{}
	previousFactory := newScreenshotCapturer
	previousAvailable := screenshotDriverAvailable
	newScreenshotCapturer = func() screenshotCapturer {
		created.Add(1)

		return capturer
	}
	screenshotDriverAvailable = func() bool { return true }

	t.Cleanup(func() {
		newScreenshotCapturer = previousFactory
		screenshotDriverAvailable = previousAvailable
	})

	SetEnrichmentPool(EnrichmentPoolConfig{Workers: 4, MaxConcurrentPerHost: 4})

	t.Cleanup(func() {
		SetEnrichmentPool(EnrichmentPoolConfig{
			Workers:              defaultEnrichmentWorkers,
			MaxConcurrentPerHost: defaultEnrichmentHostConcurrency,
			MinHostInterval:      defaultEnrichmentHostInterval,
		})
	})

	tasks := make([]EnrichmentTask, 0, 4)
	for index := range 4 {
		tasks = append(tasks, screenshotTask("task-"+string(rune('a'+index))))
	}

	repository := &enrichmentPipelineRepositoryStub{
		enrichmentRepositoryStub: &enrichmentRepositoryStub{pending: tasks},
		pending:                  1,
	}
	service := NewService(repository, t.TempDir())

	processed, err := service.processEnrichmentQueue(
		context.Background(), len(tasks), successfulAnalyzerFactory("https://example.com/"),
	)
	if err != nil || processed != len(tasks) {
		t.Fatalf("processEnrichmentQueue() = %d, %v", processed, err)
	}

	if started := created.Load(); started != 1 {
		t.Fatalf("the pass started %d browsers, want exactly 1", started)
	}
	if capturer.closed != 1 {
		t.Fatalf("the pass closed the browser %d times, want exactly 1", capturer.closed)
	}
}
