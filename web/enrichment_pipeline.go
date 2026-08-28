package web

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gosom/google-maps-scraper/web/enrichment"
)

// The enrichment queue is the second stage of a two-stage pipeline. Discovery
// walks Google Maps with browsers and commits normalized businesses; this
// stage crawls the websites those businesses point at. The two stages have
// different resource profiles — one is browser and block-risk bound, the other
// is HTTP and latency bound — so they get separate capacities.
//
// The pool never runs while a scrape is running: the worker calls it at the
// tail of its job loop, after the job's engines have shut down. What this pool
// changes is that the audits then run several at a time instead of one per
// second, bounded per host so a shared domain is never hammered.
const (
	// defaultEnrichmentWorkers is how many website audits may run at once.
	// Website work is latency bound, so a small pool converts almost linearly
	// into wall-clock time without adding CPU pressure.
	defaultEnrichmentWorkers = 4
	// maximumEnrichmentWorkers bounds any configured value. Beyond this a
	// local machine spends more on scheduling and SQLite contention than it
	// gains, and the politeness gate becomes the limit anyway.
	maximumEnrichmentWorkers = 16
	// defaultEnrichmentHostConcurrency keeps one request in flight per host.
	defaultEnrichmentHostConcurrency = 1
	// defaultEnrichmentHostInterval spaces consecutive requests to one host.
	defaultEnrichmentHostInterval = 750 * time.Millisecond
)

// EnrichmentPoolConfig bounds the website-enrichment stage. It is deliberately
// separate from the Maps worker capacity: enrichment must never take capacity
// from browser work, and browser capacity must not cap website throughput.
type EnrichmentPoolConfig struct {
	// Workers is how many website audits may run concurrently.
	Workers int
	// MaxConcurrentPerHost is how many of those may target the same host.
	MaxConcurrentPerHost int
	// MinHostInterval is the minimum spacing between two requests to one host.
	MinHostInterval time.Duration
}

// Normalized clamps the configuration to values a local machine can honour.
func (config EnrichmentPoolConfig) Normalized() EnrichmentPoolConfig {
	if config.Workers <= 0 {
		config.Workers = defaultEnrichmentWorkers
	}
	if config.Workers > maximumEnrichmentWorkers {
		config.Workers = maximumEnrichmentWorkers
	}
	if config.MaxConcurrentPerHost <= 0 {
		config.MaxConcurrentPerHost = defaultEnrichmentHostConcurrency
	}
	if config.MaxConcurrentPerHost > config.Workers {
		config.MaxConcurrentPerHost = config.Workers
	}
	if config.MinHostInterval < 0 {
		config.MinHostInterval = 0
	}

	return config
}

var (
	enrichmentPoolMutex  sync.RWMutex
	enrichmentPoolConfig = EnrichmentPoolConfig{
		Workers:              defaultEnrichmentWorkers,
		MaxConcurrentPerHost: defaultEnrichmentHostConcurrency,
		MinHostInterval:      defaultEnrichmentHostInterval,
	}
)

// SetEnrichmentPool replaces the process-wide website-enrichment capacity. It
// is process-wide rather than per-service because the resource it bounds — the
// machine's outbound HTTP capacity — is process-wide too.
func SetEnrichmentPool(config EnrichmentPoolConfig) {
	enrichmentPoolMutex.Lock()
	defer enrichmentPoolMutex.Unlock()

	enrichmentPoolConfig = config.Normalized()
}

// EnrichmentPool reports the current website-enrichment capacity.
func EnrichmentPool() EnrichmentPoolConfig {
	enrichmentPoolMutex.RLock()
	defer enrichmentPoolMutex.RUnlock()

	return enrichmentPoolConfig
}

// DomainAuditReuse is a completed audit that another business on the same
// domain may reuse instead of crawling the site again.
type DomainAuditReuse struct {
	AuditID     int64
	Domain      string
	CompletedAt time.Time
	Result      enrichment.Result
}

// JobEnrichmentTotals are the per-job counters the workspace can only know
// once website enrichment has written its evidence.
type JobEnrichmentTotals struct {
	// WebsitesFound and EmailAddresses mirror the job_runtime counters the
	// scrape's own import wrote before enrichment ran.
	WebsitesFound  int64
	EmailAddresses int64
	// BusinessesWithEmail is how many of the job's businesses now hold at
	// least one address.
	BusinessesWithEmail int64
}

// JobBusinessContacts is one job business and the addresses the workspace now
// holds for it, keyed by every identifier the legacy CSV can carry.
type JobBusinessContacts struct {
	BusinessID string
	PlaceID    string
	CID        string
	DataID     string
	Name       string
	Address    string
	Emails     []string
}

// enrichmentCacheRepository is the optional domain-audit cache. A repository
// without it simply crawls every business.
type enrichmentCacheRepository interface {
	ReusableDomainAudit(
		ctx context.Context,
		websiteURL string,
		notBefore time.Time,
		auditVersion int,
	) (DomainAuditReuse, bool, error)
}

// enrichmentTotalsRepository is the optional post-enrichment readback used to
// correct the counters the scrape wrote before any website was crawled.
type enrichmentTotalsRepository interface {
	RefreshJobEnrichmentTotals(ctx context.Context, jobID string) (JobEnrichmentTotals, error)
	PendingEnrichmentTaskCount(ctx context.Context, jobID string) (int, error)
	JobBusinessContacts(ctx context.Context, jobID string) ([]JobBusinessContacts, error)
}

// processEnrichmentQueue claims and processes up to limit durable tasks with a
// bounded worker pool. A failed task is recorded and never stops later tasks.
//
// A nil factory selects the real crawler, wired to the pass's shared host
// gate; tests pass their own factory and are unaffected by the gate.
func (s *Service) processEnrichmentQueue(
	ctx context.Context,
	limit int,
	factory websiteAnalyzerFactory,
) (int, error) {
	repository, err := s.enrichmentRepository()
	if err != nil {
		return 0, err
	}
	if limit <= 0 {
		limit = 1
	}
	if limit > maximumEnrichmentQueuePass {
		limit = maximumEnrichmentQueuePass
	}

	pool := EnrichmentPool()
	gate := enrichment.NewHostGate(enrichment.HostGateConfig{
		MaxConcurrentPerHost: pool.MaxConcurrentPerHost,
		MinInterval:          pool.MinHostInterval,
	})

	if factory == nil {
		factory = func(options EnrichmentOptions) (websiteAnalyzer, error) {
			return gatedWebsiteAnalyzer(options, gate)
		}
	}

	workers := pool.Workers
	if workers > limit {
		workers = limit
	}
	if workers < 1 {
		workers = 1
	}

	run := &enrichmentPass{
		service:    s,
		repository: repository,
		factory:    factory,
		remaining:  limit,
		jobs:       make(map[string]struct{}),
	}

	var group sync.WaitGroup

	for range workers {
		group.Add(1)

		go func() {
			defer group.Done()

			run.work(ctx)
		}()
	}

	group.Wait()
	run.closeCapturer()

	run.finalizeJobs(ctx)

	return run.processedCount(), run.err()
}

// maximumEnrichmentQueuePass bounds one pump call so a single pass can never
// monopolise the worker loop.
const maximumEnrichmentQueuePass = 25

// enrichmentPass is the shared state of one bounded queue pass.
type enrichmentPass struct {
	service    *Service
	repository enrichmentRepository
	factory    websiteAnalyzerFactory

	mutex     sync.Mutex
	remaining int
	processed int
	failures  []error
	jobs      map[string]struct{}

	// Homepage screenshots need a browser, and a browser is the one resource
	// this pool must not multiply: a pass launches at most one, shared by
	// every worker and used one capture at a time. Website enrichment running
	// several audits at once must not turn into several browsers at once.
	screenshotMutex     sync.Mutex
	capturer            screenshotCapturer
	missingDriverLogged bool
	// stop is set when the worker context is cancelled, so every worker leaves
	// its claimed task in the running state for recovery instead of recording
	// a permanent failure.
	stop bool
}

func (pass *enrichmentPass) reserve() bool {
	pass.mutex.Lock()
	defer pass.mutex.Unlock()

	if pass.stop || pass.remaining <= 0 {
		return false
	}

	pass.remaining--

	return true
}

// yield returns an unused reservation when no task was waiting.
func (pass *enrichmentPass) yield() {
	pass.mutex.Lock()
	defer pass.mutex.Unlock()

	pass.remaining = 0
}

func (pass *enrichmentPass) recordProcessed(jobID string) {
	pass.mutex.Lock()
	defer pass.mutex.Unlock()

	pass.processed++
	if jobID != "" {
		pass.jobs[jobID] = struct{}{}
	}
}

func (pass *enrichmentPass) recordFailure(err error) {
	pass.mutex.Lock()
	defer pass.mutex.Unlock()

	pass.failures = append(pass.failures, err)
}

func (pass *enrichmentPass) halt(err error) {
	pass.mutex.Lock()
	defer pass.mutex.Unlock()

	pass.stop = true
	pass.remaining = 0

	if err != nil {
		pass.failures = append(pass.failures, err)
	}
}

func (pass *enrichmentPass) processedCount() int {
	pass.mutex.Lock()
	defer pass.mutex.Unlock()

	return pass.processed
}

func (pass *enrichmentPass) err() error {
	pass.mutex.Lock()
	defer pass.mutex.Unlock()

	return errors.Join(pass.failures...)
}

func (pass *enrichmentPass) touchedJobs() []string {
	pass.mutex.Lock()
	defer pass.mutex.Unlock()

	jobs := make([]string, 0, len(pass.jobs))
	for jobID := range pass.jobs {
		jobs = append(jobs, jobID)
	}

	sort.Strings(jobs)

	return jobs
}

// work claims and runs tasks until the pass budget is spent or the queue is
// empty. Every worker shares one screenshot capturer budget of its own,
// because the capturer is not safe to share across goroutines.
func (pass *enrichmentPass) work(ctx context.Context) {
	for pass.reserve() {
		if err := ctx.Err(); err != nil {
			pass.halt(err)

			return
		}

		task, found, claimErr := pass.repository.ClaimEnrichmentTask(ctx)
		if claimErr != nil {
			pass.halt(claimErr)

			return
		}
		if !found {
			pass.yield()

			return
		}

		pass.recordProcessed(task.JobID)
		pass.runTask(ctx, task)
	}
}

// closeCapturer releases the pass's single browser, if one was ever started.
func (pass *enrichmentPass) closeCapturer() {
	pass.screenshotMutex.Lock()
	defer pass.screenshotMutex.Unlock()

	if closer, ok := pass.capturer.(interface{ Close() }); ok {
		closer.Close()
	}

	pass.capturer = nil
}

// runTask executes one claimed task: a cache reuse when the workspace already
// holds a fresh audit of the same domain, and otherwise a bounded crawl.
func (pass *enrichmentPass) runTask(ctx context.Context, task EnrichmentTask) {
	repository := pass.repository

	startedAt := time.Now().UTC()

	if reused, ok := pass.reuseDomainAudit(ctx, task); ok {
		completedAt := time.Now().UTC()

		auditID, storeErr := repository.StoreWebsiteAudit(
			context.WithoutCancel(ctx), task, reused, startedAt, completedAt,
		)
		if storeErr != nil {
			_ = repository.FinishEnrichmentTask(context.WithoutCancel(ctx), task.ID, nil, storeErr)
			pass.recordFailure(fmt.Errorf("enrichment task %s: %w", task.ID, storeErr))

			return
		}
		if finishErr := repository.FinishEnrichmentTask(
			context.WithoutCancel(ctx), task.ID, &auditID, nil,
		); finishErr != nil {
			pass.recordFailure(fmt.Errorf("finish enrichment task %s: %w", task.ID, finishErr))

			return
		}

		pass.refreshQuality(ctx, task)

		return
	}

	effective := adaptEnrichmentTimeout(ctx, repository, task)

	analyzer, analyzerErr := pass.factory(effective)
	if analyzerErr != nil {
		_ = repository.FinishEnrichmentTask(ctx, task.ID, nil, analyzerErr)
		pass.recordFailure(fmt.Errorf("enrichment task %s: %w", task.ID, analyzerErr))

		return
	}

	startedAt = time.Now().UTC()
	// The whole-task budget derives from the same per-request timeout the
	// analyzer received, so an adapted (shorter) budget frees the worker
	// sooner and can never widen the envelope.
	requestBudget := effective.requestTimeout() *
		time.Duration(effective.MaxPages+effective.MaxInternalLinkChecks+2)
	if requestBudget < 30*time.Second {
		requestBudget = 30 * time.Second
	}
	if requestBudget > 30*time.Minute {
		requestBudget = 30 * time.Minute
	}

	taskContext, cancelTask := context.WithTimeout(ctx, requestBudget)
	result, analyzeErr := analyzer.Analyze(taskContext, task.WebsiteURL)

	cancelTask()

	completedAt := time.Now().UTC()

	if analyzeErr != nil {
		// A cancelled worker context means the application is shutting down,
		// not that the website failed. Leave the claimed task running so
		// RecoverEnrichmentTasks requeues it on the next start; recording a
		// permanent failure here would silently discard the work instead.
		if ctx.Err() != nil {
			pass.halt(fmt.Errorf("enrichment task %s: %w", task.ID, ctx.Err()))

			return
		}
		if errors.Is(analyzeErr, enrichment.ErrUnsafeURL) ||
			errors.Is(analyzeErr, enrichment.ErrUnsupportedScheme) {
			_ = repository.FinishEnrichmentTask(context.WithoutCancel(ctx), task.ID, nil, analyzeErr)
			pass.recordFailure(fmt.Errorf("enrichment task %s: %w", task.ID, analyzeErr))

			return
		}
		// DNS, timeout, TLS, and transport failures are useful website-status
		// evidence. Persist them as an inaccessible audit rather than losing
		// the observation in a task error alone.
		result = enrichment.Result{RequestedURL: task.WebsiteURL, Error: analyzeErr.Error()}
	}

	result.AuditVersion = enrichment.AuditVersion

	auditID, storeErr := repository.StoreWebsiteAudit(
		context.WithoutCancel(ctx), task, result, startedAt, completedAt,
	)
	if storeErr != nil {
		_ = repository.FinishEnrichmentTask(context.WithoutCancel(ctx), task.ID, nil, storeErr)
		pass.recordFailure(fmt.Errorf("enrichment task %s: %w", task.ID, storeErr))

		return
	}
	if finishErr := repository.FinishEnrichmentTask(
		context.WithoutCancel(ctx), task.ID, &auditID, nil,
	); finishErr != nil {
		pass.recordFailure(fmt.Errorf("finish enrichment task %s: %w", task.ID, finishErr))

		return
	}

	// The homepage screenshot is best-effort extra evidence captured only for
	// genuinely reachable audits. It runs after the task is durably completed
	// so a slow browser can never hold an audit hostage.
	if task.Options.CaptureScreenshot {
		pass.captureScreenshots(ctx, task, auditID, result, analyzeErr)
	}

	pass.refreshQuality(ctx, task)
}

// reuseDomainAudit returns a fresh audit of the same domain when the workspace
// already holds one. Two businesses that share a website — a franchise, a
// directory page, a chain — then cost one crawl rather than one crawl each.
func (pass *enrichmentPass) reuseDomainAudit(
	ctx context.Context,
	task EnrichmentTask,
) (enrichment.Result, bool) {
	if task.Options.Force || task.Options.StaleAfterHours <= 0 {
		return enrichment.Result{}, false
	}

	cache, supported := pass.repository.(enrichmentCacheRepository)
	if !supported {
		return enrichment.Result{}, false
	}

	notBefore := time.Now().UTC().Add(-time.Duration(task.Options.StaleAfterHours) * time.Hour)

	entry, found, err := cache.ReusableDomainAudit(
		ctx, task.WebsiteURL, notBefore, enrichment.AuditVersion,
	)
	if err != nil || !found {
		return enrichment.Result{}, false
	}

	result := entry.Result
	result.RequestedURL = task.WebsiteURL
	result.AuditVersion = enrichment.AuditVersion
	result.Cache = &enrichment.CacheProvenance{
		ReusedFromAuditID: entry.AuditID,
		Domain:            entry.Domain,
		ObservedAt:        entry.CompletedAt,
	}

	return result, true
}

// captureScreenshots takes the pass's single browser for the duration of one
// task's captures. Serialising here is deliberate: the audits are what the pool
// parallelises, and a browser per worker would spend more memory on evidence
// than on the work.
func (pass *enrichmentPass) captureScreenshots(
	ctx context.Context,
	task EnrichmentTask,
	auditID int64,
	result enrichment.Result,
	analyzeErr error,
) {
	pass.screenshotMutex.Lock()
	defer pass.screenshotMutex.Unlock()

	if !screenshotDriverAvailable() {
		if !pass.missingDriverLogged {
			pass.missingDriverLogged = true
			_ = pass.repository.RecordScreenshotEvent(
				context.WithoutCancel(ctx),
				"screenshot_skipped_no_driver",
				task.ID,
				`{"reason":"the Playwright browser driver is not installed on this host"}`,
			)
		}

		return
	}

	if pass.capturer == nil {
		pass.capturer = newScreenshotCapturer()
	}

	if analyzeErr == nil {
		pass.service.captureAuditScreenshot(ctx, pass.repository, pass.capturer, task, auditID, result.FinalURL)
	}
	if shouldCaptureErrorScreenshot(result) {
		pass.service.captureAuditErrorScreenshot(ctx, pass.repository, pass.capturer, task, auditID, result.FinalURL)
	}
}

func (pass *enrichmentPass) refreshQuality(ctx context.Context, task EnrichmentTask) {
	if _, err := pass.service.RecalculateQuality(
		context.WithoutCancel(ctx), []string{task.BusinessID},
	); err != nil && !errors.Is(err, ErrQualityScoringUnsupported) {
		pass.recordFailure(fmt.Errorf("refresh quality for %s: %w", task.BusinessID, err))
	}
}

// finalizeJobs corrects the per-job truth for every job whose website audits
// this pass drained.
//
// The scrape's own import writes job_runtime.emails_found and the per-job CSV
// before a single website has been crawled, so a job whose emails all come from
// enrichment reported zero emails and exported none while holding real
// addresses. Both are rewritten here, once the job's queue is genuinely empty.
func (pass *enrichmentPass) finalizeJobs(ctx context.Context) {
	totals, supported := pass.repository.(enrichmentTotalsRepository)
	if !supported {
		return
	}

	for _, jobID := range pass.touchedJobs() {
		pending, err := totals.PendingEnrichmentTaskCount(context.WithoutCancel(ctx), jobID)
		if err != nil {
			pass.recordFailure(fmt.Errorf("count pending enrichment for job %s: %w", jobID, err))

			continue
		}
		if pending > 0 {
			// More audits are still queued: the job's numbers are not final
			// yet, and rewriting them now would report a moving total as done.
			continue
		}

		if _, err := totals.RefreshJobEnrichmentTotals(context.WithoutCancel(ctx), jobID); err != nil {
			pass.recordFailure(fmt.Errorf("refresh enrichment totals for job %s: %w", jobID, err))

			continue
		}

		updated, err := pass.service.backfillJobResultEmails(context.WithoutCancel(ctx), jobID, totals)
		if err != nil {
			pass.recordFailure(fmt.Errorf("write enriched emails into the result file for job %s: %w", jobID, err))

			continue
		}

		pass.recordEnrichmentFinished(ctx, jobID, updated)
	}
}

// recordEnrichmentFinished writes the durable evidence that the second stage
// is over. The job's own status flips to a terminal value as soon as the
// listing walk ends, minutes before the website audits do, so without this the
// operator has no record of when the run actually finished.
func (pass *enrichmentPass) recordEnrichmentFinished(ctx context.Context, jobID string, rowsUpdated int) {
	_ = pass.service.RecordJobWorkerEvent(
		context.WithoutCancel(ctx),
		jobID,
		"enrichment-complete",
		"information",
		"Website enrichment finished for this job",
		map[string]any{"result_rows_updated": rowsUpdated},
	)
}

// backfillJobResultEmails writes the addresses the workspace holds into the
// job's legacy CSV.
//
// The file is the export path, and it is written during the scrape, before
// enrichment has found anything. Rewriting it is strictly additive: the header
// and the column order are untouched, every other cell is copied verbatim, and
// an existing address is kept and merged rather than replaced. The new file is
// written beside the original and renamed over it, so a failure at any point
// leaves the original intact.
func (s *Service) backfillJobResultEmails(
	ctx context.Context,
	jobID string,
	repository enrichmentTotalsRepository,
) (int, error) {
	path, err := s.csvPath(jobID)
	if err != nil {
		return 0, err
	}
	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
		return 0, nil
	} else if statErr != nil {
		return 0, fmt.Errorf("inspect result file: %w", statErr)
	}

	contacts, err := repository.JobBusinessContacts(ctx, jobID)
	if err != nil {
		return 0, fmt.Errorf("read job contacts: %w", err)
	}

	index := newContactIndex(contacts)
	if index.empty() {
		return 0, nil
	}

	source, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open result file: %w", err)
	}

	// The handle is closed before the replacement is renamed into place:
	// Windows refuses to rename over a file that is still open.
	sourceClosed := false

	defer func() {
		if !sourceClosed {
			_ = source.Close()
		}
	}()

	reader := csv.NewReader(source)
	reader.FieldsPerRecord = -1

	header, err := reader.Read()
	if err != nil {
		// An empty or unreadable file is not something to repair here.
		return 0, nil //nolint:nilerr // a missing header means there is nothing to rewrite
	}

	columns := make(map[string]int, len(header))
	for position, name := range header {
		columns[strings.ToLower(strings.TrimSpace(name))] = position
	}

	emailColumn, hasEmailColumn := columns["emails"]
	if !hasEmailColumn {
		return 0, nil
	}

	rows := make([][]string, 0, 256)
	updated := 0

	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}

		row, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return 0, fmt.Errorf("read result row: %w", readErr)
		}

		if emailColumn < len(row) {
			merged := index.merge(cell(row, columns, "place_id"), cell(row, columns, "cid"),
				cell(row, columns, "data_id"), cell(row, columns, "title"),
				cell(row, columns, "address"), row[emailColumn])
			if merged != "" && merged != row[emailColumn] {
				row[emailColumn] = merged
				updated++
			}
		}

		rows = append(rows, row)
	}

	if err := source.Close(); err != nil {
		return 0, fmt.Errorf("close result file: %w", err)
	}

	sourceClosed = true

	if updated == 0 {
		return 0, nil
	}

	if err := writeResultRows(path, header, rows); err != nil {
		return 0, err
	}

	return updated, nil
}

// writeResultRows replaces one result file atomically, preserving its header
// and column order exactly.
func writeResultRows(path string, header []string, rows [][]string) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".enriched-*")
	if err != nil {
		return fmt.Errorf("create replacement result file: %w", err)
	}

	temporaryPath := temporary.Name()
	removeTemporary := true

	defer func() {
		if removeTemporary {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
		}
	}()

	writer := csv.NewWriter(temporary)
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("write result header: %w", err)
	}

	for _, row := range rows {
		if err := writer.Write(row); err != nil {
			return fmt.Errorf("write result row: %w", err)
		}
	}

	writer.Flush()

	if err := writer.Error(); err != nil {
		return fmt.Errorf("flush result rows: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("flush result file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close replacement result file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace result file: %w", err)
	}

	removeTemporary = false

	return nil
}

func cell(row []string, columns map[string]int, name string) string {
	position, found := columns[name]
	if !found || position < 0 || position >= len(row) {
		return ""
	}

	return strings.TrimSpace(row[position])
}

// contactIndex resolves a legacy CSV row to the addresses the workspace holds,
// using the same identifier precedence the result importer uses.
type contactIndex struct {
	byKey map[string][]string
}

func newContactIndex(contacts []JobBusinessContacts) contactIndex {
	index := contactIndex{byKey: make(map[string][]string, len(contacts)*2)}

	for _, contact := range contacts {
		// Stored addresses are passed through the current hygiene rules before
		// they are exported. A workspace can hold rows written by an older
		// extractor — page text welded onto a real mailbox — and an export is
		// exactly where such a value does damage, because it is what somebody
		// mails. Refused values are dropped from the file and repaired ones are
		// exported in their repaired form; neither changes the stored row,
		// which the hygiene report accounts for separately.
		usable := make([]string, 0, len(contact.Emails))

		for _, address := range contact.Emails {
			verdict := enrichment.ClassifyStoredEmail(address)
			if verdict.Rejected {
				continue
			}

			usable = append(usable, verdict.Address)
		}

		if len(usable) == 0 {
			continue
		}

		for _, key := range contactKeys(contact.PlaceID, contact.CID, contact.DataID,
			contact.Name, contact.Address) {
			index.byKey[key] = usable
		}
	}

	return index
}

func (index contactIndex) empty() bool {
	return len(index.byKey) == 0
}

// merge unions the addresses already in the file with the ones the workspace
// holds, preserving the file's existing order first so nothing is ever lost.
func (index contactIndex) merge(placeID, cid, dataID, title, address, existing string) string {
	var stored []string

	for _, key := range contactKeys(placeID, cid, dataID, title, address) {
		if values, found := index.byKey[key]; found {
			stored = values

			break
		}
	}

	if len(stored) == 0 {
		return existing
	}

	merged := make([]string, 0, len(stored)+2)
	seen := make(map[string]struct{}, len(stored)+2)

	for _, value := range append(splitCSVEmails(existing), stored...) {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}

		key := strings.ToLower(value)
		if _, duplicate := seen[key]; duplicate {
			continue
		}

		seen[key] = struct{}{}

		merged = append(merged, value)
	}

	return strings.Join(merged, ", ")
}

// contactKeys returns the identifiers a CSV row and a stored business can be
// matched on, most specific first.
func contactKeys(placeID, cid, dataID, title, address string) []string {
	keys := make([]string, 0, 4)

	if placeID = strings.TrimSpace(placeID); placeID != "" {
		keys = append(keys, "place:"+placeID)
	}
	if cid = strings.TrimSpace(cid); cid != "" {
		keys = append(keys, "cid:"+cid)
	}
	if dataID = strings.TrimSpace(dataID); dataID != "" {
		keys = append(keys, "data:"+dataID)
	}

	title = strings.TrimSpace(title)
	address = strings.TrimSpace(address)

	if title != "" || address != "" {
		keys = append(keys, "name-address:"+strings.ToLower(title+"\x00"+address))
	}

	return keys
}

// splitCSVEmails reads the legacy column format, which the scraper writes as a
// comma separated list and older files may wrap in brackets.
func splitCSVEmails(value string) []string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "[")
	value = strings.TrimSuffix(value, "]")

	parts := strings.FieldsFunc(value, func(character rune) bool {
		return character == ',' || character == ';' || character == ' ' ||
			character == '\t' || character == '\n'
	})

	return parts
}

// gatedWebsiteAnalyzer builds the real analyzer with the pass's shared host
// gate attached, so every worker's requests share one per-host budget.
func gatedWebsiteAnalyzer(
	options EnrichmentOptions,
	gate enrichment.HostGate,
) (websiteAnalyzer, error) {
	config := options.crawlerConfig()
	config.HostGate = gate

	if options.Preclassify {
		return preclassifyAnalyzer{config: config}, nil
	}

	return enrichment.NewCrawler(config)
}

// MaximumEmailHygieneSamples bounds how many example addresses a hygiene
// report carries, so the response stays small on a large workspace.
const MaximumEmailHygieneSamples = 25

// EmailHygieneSample is one stored address the current rules would change.
type EmailHygieneSample struct {
	Value      string `json:"value"`
	Reason     string `json:"reason,omitempty"`
	Repaired   string `json:"repaired,omitempty"`
	BusinessID string `json:"business_id"`
}

// EmailHygieneReport counts the stored addresses the current extraction rules
// would refuse or repair.
//
// It answers a specific question honestly: how much of what the workspace
// already presents as a contact is page text that was welded onto an address.
// Nothing is deleted on the strength of it; re-auditing the affected
// businesses is what replaces the evidence.
type EmailHygieneReport struct {
	// Total is how many stored addresses were classified.
	Total int64 `json:"total"`
	// Unusable is how many the rules would refuse outright.
	Unusable int64 `json:"unusable"`
	// Repairable is how many carry glued text the rules would now trim.
	Repairable int64 `json:"repairable"`
	// Reasons counts the refusals by named reason.
	Reasons map[string]int64 `json:"reasons,omitempty"`
	// Methods counts the affected rows by the extraction method that produced
	// them, which is what identifies the responsible extractor.
	Methods map[string]int64 `json:"extraction_methods,omitempty"`
	// Samples are bounded examples for the operator to inspect.
	Samples []EmailHygieneSample `json:"samples,omitempty"`
}

// Affected is how many stored addresses the current rules would change.
func (report EmailHygieneReport) Affected() int64 {
	return report.Unusable + report.Repairable
}

type emailHygieneRepository interface {
	EnrichmentEmailHygieneReport(ctx context.Context) (EmailHygieneReport, error)
}

// EmailHygieneReport classifies every stored address against the current
// extraction rules without changing any of them.
func (s *Service) EmailHygieneReport(ctx context.Context) (EmailHygieneReport, error) {
	repository, ok := s.repo.(emailHygieneRepository)
	if !ok {
		return EmailHygieneReport{}, ErrEnrichmentUnsupported
	}

	return repository.EnrichmentEmailHygieneReport(ctx)
}

// ReconcileJobEnrichment recomputes one finished job's contact counters and
// rewrites its result file from the evidence the workspace holds.
//
// It exists for jobs that finished before this reconciliation ran at all: their
// counters and their export were written mid-scrape and never corrected, so
// they still claim zero emails while the workspace holds real addresses. It is
// idempotent and additive; nothing is deleted.
func (s *Service) ReconcileJobEnrichment(ctx context.Context, jobID string) (JobEnrichmentReconciliation, error) {
	repository, ok := s.repo.(enrichmentTotalsRepository)
	if !ok {
		return JobEnrichmentReconciliation{}, ErrEnrichmentUnsupported
	}

	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return JobEnrichmentReconciliation{}, fmt.Errorf("%w: a job ID is required", ErrInvalidEnrichment)
	}

	pending, err := repository.PendingEnrichmentTaskCount(ctx, jobID)
	if err != nil {
		return JobEnrichmentReconciliation{}, err
	}

	totals, err := repository.RefreshJobEnrichmentTotals(ctx, jobID)
	if err != nil {
		return JobEnrichmentReconciliation{}, err
	}

	updated, err := s.backfillJobResultEmails(ctx, jobID, repository)
	if err != nil {
		return JobEnrichmentReconciliation{}, err
	}

	return JobEnrichmentReconciliation{
		JobID:               jobID,
		PendingAudits:       pending,
		WebsitesFound:       totals.WebsitesFound,
		EmailAddresses:      totals.EmailAddresses,
		BusinessesWithEmail: totals.BusinessesWithEmail,
		ResultRowsUpdated:   updated,
	}, nil
}

// JobEnrichmentReconciliation reports what one reconciliation corrected.
type JobEnrichmentReconciliation struct {
	JobID string `json:"job_id"`
	// PendingAudits is how many of the job's website audits had still not
	// finished. A non-zero value means these numbers will change again.
	PendingAudits       int   `json:"pending_audits"`
	WebsitesFound       int64 `json:"websites_found"`
	EmailAddresses      int64 `json:"email_addresses"`
	BusinessesWithEmail int64 `json:"businesses_with_email"`
	// ResultRowsUpdated is how many rows of the job's result file gained an
	// address that the workspace already held.
	ResultRowsUpdated int `json:"result_rows_updated"`
}
