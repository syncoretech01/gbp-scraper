package webrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/deduper"
	"github.com/gosom/google-maps-scraper/exiter"
	"github.com/gosom/google-maps-scraper/gmaps"
	"github.com/gosom/google-maps-scraper/runner"
	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/prospect"
	"github.com/gosom/scrapemate"
)

// coverageExpansionBatch bounds how many neighbour ZIPs one productive task
// may add at once; the job-level budget (MaxExpansions) still applies on top.
const coverageExpansionBatch = 3

// coverageEngine holds the mid-run adaptive discovery state of one pool run:
// the saturation window, the ZIPs the plan already covers, and the expansion
// budget. All durable effects (skipping, appending) are performed by the
// webrunner from the engine's decisions, so the engine itself stays pure and
// easily testable.
type coverageEngine struct {
	mu sync.Mutex

	jobID   string
	options web.CoverageOptions

	window          []web.CoverageSample
	known           map[string]struct{}
	nextSequence    int
	expansionsAdded int
	saturated       bool

	// zipAreas is swappable in tests; production uses the embedded dataset.
	zipAreas func() []prospect.ZIPArea
	zipIndex map[string]prospect.ZIPArea

	// blindspot holds the discovery blind-spot guards: truncated-cell
	// refinement and adjacent-ZIP overlap. See coverage_refine.go.
	blindspot coverageBlindspotState
}

// coverageDecision is what one finished task attempt changed.
type coverageDecision struct {
	// recorded reports whether the attempt contributed evidence to the
	// saturation window. Only attempts that completed without error do.
	recorded bool
	// saturatedNow is true exactly once: on the completion whose window
	// crossed a stop threshold.
	saturatedNow bool
	// reason names which rule fired: web.CoverageSaturationReasonDuplicates
	// or web.CoverageSaturationReasonEmpty. It is empty unless
	// saturatedNow is set.
	reason     string
	ratio      float64
	windowSize int
	evidence   web.CoverageWindowEvidence
	expansions []web.JobTaskDefinition
	// refinements re-cover a cell whose own result set looked truncated.
	// They draw on the same MaxExpansions budget as expansions.
	refinements []web.JobTaskDefinition
	parentZIP   string
}

func newCoverageEngine(jobID string, options web.CoverageOptions, seed web.CoverageSeedState) *coverageEngine {
	engine := &coverageEngine{
		jobID:           jobID,
		options:         options,
		known:           make(map[string]struct{}),
		nextSequence:    seed.MaxSequence + 1,
		expansionsAdded: seed.ExpansionTasks,
		zipAreas:        prospect.EmbeddedZIPAreas,
	}

	for _, query := range seed.Queries {
		if zip, ok := web.ParseGBPQueryZIP(query); ok {
			engine.known[zip] = struct{}{}
		}
	}

	return engine
}

// recordCompletion folds one SUCCESSFUL task into the saturation window and
// decides whether the run just saturated or earned an expansion. Only tasks
// that completed without error may be recorded here; route every failed
// attempt through recordFailedAttempt instead.
func (engine *coverageEngine) recordCompletion(task web.JobTask, checkpoint web.JobTaskCheckpoint) coverageDecision {
	return engine.record(task, checkpoint, true)
}

// recordFailedAttempt folds one FAILED task attempt into the engine. A
// browser crash, proxy error, timeout or parsing failure says nothing about
// whether an area still holds businesses, so the attempt never enters the
// saturation window, never evicts the good evidence already in it, never
// stops the plan and never earns an expansion. Routing failures through the
// engine makes that an enforced invariant rather than a property of the call
// site.
func (engine *coverageEngine) recordFailedAttempt(
	task web.JobTask,
	checkpoint web.JobTaskCheckpoint,
) coverageDecision {
	return engine.record(task, checkpoint, false)
}

// record is the single door into the saturation window; succeeded decides
// whether the attempt is evidence at all.
func (engine *coverageEngine) record(
	task web.JobTask,
	checkpoint web.JobTaskCheckpoint,
	succeeded bool,
) coverageDecision {
	engine.mu.Lock()
	defer engine.mu.Unlock()

	windowSize := engine.options.WindowOrDefault()

	if succeeded {
		engine.window = append(engine.window, web.NewCoverageSample(
			checkpoint.RowsAdded, checkpoint.RowsReplaced,
			checkpoint.DuplicatesSkipped, true,
		))

		if len(engine.window) > windowSize {
			engine.window = engine.window[len(engine.window)-windowSize:]
		}
	}

	decision := coverageDecision{
		recorded:   succeeded,
		ratio:      web.CoverageWindowRatio(engine.window),
		windowSize: len(engine.window),
		evidence:   web.CoverageWindowEvidenceOf(engine.window),
	}

	if !succeeded || engine.saturated {
		return decision
	}

	// Sustained successful zero yield: a full window of clean successes that
	// returned neither new rows nor duplicates is genuine exhaustion. The
	// ratio rule cannot see this case at all — a window with no rows and no
	// duplicates scores a perfect 1.0 — so it is checked first. A partial
	// window, any failed entry, or any productive entry disqualifies it.
	if engine.options.StopOnEmptyWindowOrDefault() && decision.evidence.ZeroYield(windowSize) {
		engine.saturated = true
		decision.saturatedNow = true
		decision.reason = web.CoverageSaturationReasonEmpty

		return decision
	}

	// Duplicate saturation: unchanged historical rule. A mixed window of
	// empty and productive tasks is judged by this ratio alone.
	if engine.options.AutoStop && len(engine.window) >= windowSize &&
		decision.ratio < engine.options.MinNewRatioOrDefault() {
		engine.saturated = true
		decision.saturatedNow = true
		decision.reason = web.CoverageSaturationReasonDuplicates

		return decision
	}

	// A truncated cell is refined before any budget reaches its neighbours:
	// widening the search while the current cell is still cut off would
	// compound the blind spot rather than close it.
	engine.refineLocked(task, checkpoint, &decision)
	engine.expandLocked(task, checkpoint, &decision)

	return decision
}

// expandLocked appends nearest-neighbour tasks for a productive GBP-shaped
// task, within the job's expansion budget. Callers hold engine.mu.
func (engine *coverageEngine) expandLocked(
	task web.JobTask,
	checkpoint web.JobTaskCheckpoint,
	decision *coverageDecision,
) {
	if engine.options.MaxExpansions <= 0 || engine.expansionsAdded >= engine.options.MaxExpansions {
		return
	}

	if coverageNetNewRows(checkpoint) < int64(engine.options.ExpansionMinNewOrDefault()) {
		return
	}

	synonym, parentZIP, ok := web.SplitGBPQuery(task.Query)
	if !ok {
		return
	}

	if engine.zipIndex == nil {
		areas := engine.zipAreas()
		engine.zipIndex = make(map[string]prospect.ZIPArea, len(areas))

		for _, area := range areas {
			engine.zipIndex[area.ZIP] = area
		}
	}

	parent, found := engine.zipIndex[parentZIP]
	if !found {
		return
	}

	// The parent ZIP itself is covered by definition, even when its query
	// was not parsed at seed time.
	engine.known[parentZIP] = struct{}{}

	// One neighbourhood per parent ZIP, however many synonyms cross it.
	if !engine.claimParentExpansionLocked(parentZIP) {
		return
	}

	type candidate struct {
		area     prospect.ZIPArea
		distance float64
	}

	candidates := make([]candidate, 0, 64)

	for _, area := range engine.zipAreas() {
		if area.State != parent.State || area.ZIP == parent.ZIP {
			continue
		}

		if _, covered := engine.known[area.ZIP]; covered {
			continue
		}

		// A neighbour whose centroid nearly coincides with one the plan
		// already covers would re-find the same businesses.
		if engine.overlapsCoveredZIPLocked(area) {
			continue
		}

		candidates = append(candidates, candidate{
			area:     area,
			distance: haversineKM(parent.Latitude, parent.Longitude, area.Latitude, area.Longitude),
		})
	}

	if len(candidates) == 0 {
		return
	}

	sort.Slice(candidates, func(a, b int) bool {
		if candidates[a].distance == candidates[b].distance {
			return candidates[a].area.ZIP < candidates[b].area.ZIP
		}

		return candidates[a].distance < candidates[b].distance
	})

	budget := engine.options.MaxExpansions - engine.expansionsAdded
	if budget > coverageExpansionBatch {
		budget = coverageExpansionBatch
	}

	if budget > len(candidates) {
		budget = len(candidates)
	}

	decision.parentZIP = parentZIP

	// Candidates are re-checked as the batch fills: two neighbours can each
	// clear the parent yet nearly coincide with each other.
	for _, chosen := range candidates {
		if len(decision.expansions) >= budget {
			break
		}

		if engine.overlapsCoveredZIPLocked(chosen.area) {
			continue
		}

		definition := expansionTaskDefinition(engine.jobID, synonym, parentZIP, chosen.area, engine.nextSequence)
		engine.nextSequence++
		engine.expansionsAdded++
		engine.known[chosen.area.ZIP] = struct{}{}
		decision.expansions = append(decision.expansions, definition)
	}
}

// expansionTaskPayload is the durable, restart-safe description of one
// expansion task. The worker rebuilds the seed from this payload alone, so
// an expansion task executes identically before and after a process restart.
type expansionTaskPayload struct {
	SeedID      string `json:"seed_id"`
	Sequence    int    `json:"sequence"`
	Query       string `json:"query"`
	Coordinates string `json:"coordinates"`
	ZIP         string `json:"zip"`
	ParentZIP   string `json:"parent_zip"`
	Expansion   bool   `json:"expansion"`
	// Refinement marks a task that re-covers its own cell rather than a
	// neighbour, and Zoom is the tighter zoom it does that at. Both are
	// additive: a payload written before refinements existed decodes to
	// false and zero, and then behaves exactly as it always did.
	Refinement bool `json:"refinement,omitempty"`
	Zoom       int  `json:"zoom,omitempty"`
}

func expansionTaskDefinition(
	jobID, synonym, parentZIP string,
	area prospect.ZIPArea,
	sequence int,
) web.JobTaskDefinition {
	query := strings.Join(strings.Fields(fmt.Sprintf(
		"%s in %s %s %s", synonym, area.City, strings.ToUpper(area.State), area.ZIP,
	)), " ")

	identity := strings.Join([]string{"coverage-expansion", jobID, query}, "\x1f")
	key := "exp-" + uuid.NewSHA1(uuid.NameSpaceURL, []byte(identity)).String()

	payload, err := json.Marshal(expansionTaskPayload{
		SeedID:      key,
		Sequence:    sequence,
		Query:       query,
		Coordinates: fmt.Sprintf("%.6f,%.6f", area.Latitude, area.Longitude),
		ZIP:         area.ZIP,
		ParentZIP:   parentZIP,
		Expansion:   true,
	})
	if err != nil {
		payload = []byte("{}")
	}

	return web.JobTaskDefinition{
		Key:      key,
		Kind:     "map-query",
		Sequence: sequence,
		Query:    query,
		Payload:  payload,
		Origin:   web.CoverageExpansionOriginPrefix + parentZIP,
		Priority: 1,
	}
}

// buildExpansionSeed reconstructs the scrape seed of a coverage-expansion
// task purely from its durable payload, so the task runs identically whether
// it was appended this run or resumed after a restart. It returns (nil, nil)
// for tasks that are not expansion tasks.
func buildExpansionSeed(
	job *web.Job,
	task web.JobTask,
	dedup deduper.Deduper,
	exitMonitor exiter.Exiter,
	extraReviews bool,
) (scrapemate.IJob, error) {
	if !isCoverageAppendedOrigin(task.Origin) {
		return nil, nil
	}

	var payload expansionTaskPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return nil, fmt.Errorf("decode expansion task %q payload: %w", task.Key, err)
	}

	if !payload.Expansion || payload.Query == "" {
		return nil, fmt.Errorf("expansion task %q has an incomplete payload", task.Key)
	}

	seedID := payload.SeedID
	if seedID == "" {
		seedID = task.Key
	}

	options := make([]gmaps.GmapJobOptions, 0, 3)
	if dedup != nil {
		options = append(options, gmaps.WithDeduper(dedup))
	}

	if exitMonitor != nil {
		options = append(options, gmaps.WithExitMonitor(exitMonitor))
	}

	if extraReviews {
		options = append(options, gmaps.WithExtraReviews())
	}

	seed := gmaps.NewGmapJob(
		seedID,
		job.Data.Lang,
		payload.Query,
		job.Data.Depth,
		job.Data.Email,
		payload.Coordinates,
		resolveSeedZoom(job.Data.Zoom, payload.Zoom),
		options...,
	)

	runner.ConfigureSeedRuntime([]scrapemate.IJob{seed}, runner.SeedRuntimeOptions{
		Timeout:           job.Data.PageTimeout,
		MaxRetries:        job.Data.RetryCount,
		MaxRetryDelay:     job.Data.RetryDelay,
		RetriesConfigured: job.Data.RetryConfigured,
		RandomDelayMin:    job.Data.RandomDelayMin,
		RandomDelayMax:    job.Data.RandomDelayMax,
	})

	return seed, nil
}

// applyCoverageFailure hands one failed task attempt to the coverage engine,
// which rejects it as evidence. It has no durable effect by construction:
// the attempt is not added to the saturation window, so it can neither stop
// the plan nor push older, good evidence out of the window.
func (w *webrunner) applyCoverageFailure(
	run *taskPoolRun,
	task web.JobTask,
	checkpoint web.JobTaskCheckpoint,
) {
	engine := run.coverage
	if engine == nil {
		return
	}

	_ = engine.recordFailedAttempt(task, checkpoint)
}

// coverageStopEvent maps a saturation reason to the durable skip reason
// written on every skipped task and to the worker event type operators see.
// The two stops are reported separately so "this area is empty" is never
// confused with "we keep re-finding the same businesses".
func coverageStopEvent(reason string) (skipReason, eventType string) {
	if reason == web.CoverageSaturationReasonEmpty {
		return web.CoverageEmptySkipReason, "coverage-exhausted"
	}

	return web.CoverageSkipReason, "coverage-saturated"
}

// applySaturationStop skips the remaining plan and records the worker event
// for whichever saturation rule fired.
func (w *webrunner) applySaturationStop(jobID string, engine *coverageEngine, decision coverageDecision) {
	skipReason, eventType := coverageStopEvent(decision.reason)

	skipped, skipErr := w.svc.SkipPendingJobTasks(context.Background(), jobID, skipReason)
	if skipErr != nil {
		_ = w.svc.RecordJobWorkerEvent(
			context.Background(), jobID, eventType, "warning",
			"Coverage saturation was detected but the remaining plan could not be skipped",
			map[string]any{"error": skipErr.Error()},
		)

		return
	}

	fields := map[string]any{
		"window":             decision.windowSize,
		"current_new_ratio":  decision.ratio,
		"min_new_ratio":      engine.options.MinNewRatioOrDefault(),
		"skipped_tasks":      skipped,
		"reason":             decision.reason,
		"successful_samples": decision.evidence.Successful,
		"empty_samples":      decision.evidence.Empty,
	}

	message := fmt.Sprintf(
		"Coverage saturated: only %.0f%% of the last %d task(s) produced new rows; %d remaining task(s) were skipped",
		decision.ratio*100, decision.windowSize, skipped,
	)

	if decision.reason == web.CoverageSaturationReasonEmpty {
		message = fmt.Sprintf(
			"Coverage exhausted: the last %d task(s) all succeeded with no new and no duplicate rows; "+
				"%d remaining task(s) were skipped",
			decision.windowSize, skipped,
		)
	}

	_ = w.svc.RecordJobWorkerEvent(context.Background(), jobID, eventType, "information", message, fields)
}

// applyCoverage performs the durable side of one coverage decision: it skips
// the remaining plan on saturation, or appends expansion tasks, and records
// the corresponding worker events.
//
// It is called for tasks that completed without error only, which is what
// keeps the saturation window made of clean evidence. Failed attempts go to
// applyCoverageFailure.
func (w *webrunner) applyCoverage(
	run *taskPoolRun,
	task web.JobTask,
	checkpoint web.JobTaskCheckpoint,
	exitMonitor exiter.Exiter,
) {
	engine := run.coverage
	if engine == nil {
		return
	}

	job := run.job
	decision := engine.recordCompletion(task, checkpoint)

	if decision.saturatedNow {
		w.applySaturationStop(job.ID, engine, decision)

		return
	}

	w.appendCoverageRefinements(run, checkpoint, decision, exitMonitor)

	if len(decision.expansions) == 0 {
		return
	}

	maximumAttempts := 1
	if job.Data.RetryConfigured {
		maximumAttempts = max(1, job.Data.RetryCount+1)
	}

	inserted, appendErr := w.svc.AppendJobTasks(context.Background(), job.ID, decision.expansions, maximumAttempts)
	if appendErr != nil {
		_ = w.svc.RecordJobWorkerEvent(
			context.Background(), job.ID, "coverage-expanded", "warning",
			"Coverage expansion tasks could not be appended to the plan",
			map[string]any{"error": appendErr.Error(), "parent_zip": decision.parentZIP},
		)

		return
	}

	if len(inserted) == 0 {
		return
	}

	// The exit monitor must expect the appended seeds, otherwise it would
	// conclude the run once the original plan finishes.
	snapshot := exiter.SnapshotOf(exitMonitor)
	exitMonitor.SetSeedCount(snapshot.SeedsTotal + len(inserted))

	zips := make([]string, 0, len(inserted))

	for _, appended := range inserted {
		if _, zip, ok := web.SplitGBPQuery(appended.Query); ok {
			zips = append(zips, zip)
		}
	}

	_ = w.svc.RecordJobWorkerEvent(
		context.Background(), job.ID, "coverage-expanded", "information",
		fmt.Sprintf(
			"Coverage expanded: %d nearby ZIP task(s) were appended after %d new row(s) around ZIP %s",
			len(inserted), checkpoint.RowsAdded, decision.parentZIP,
		),
		map[string]any{
			"parent_zip": decision.parentZIP,
			"zips":       zips,
			"appended":   len(inserted),
			"rows_added": checkpoint.RowsAdded,
		},
	)
}

// haversineKM is the great-circle distance between two coordinates.
func haversineKM(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadiusKM = 6371.0

	toRadians := func(degrees float64) float64 { return degrees * math.Pi / 180 }

	dLat := toRadians(lat2 - lat1)
	dLon := toRadians(lon2 - lon1)

	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(toRadians(lat1))*math.Cos(toRadians(lat2))*math.Sin(dLon/2)*math.Sin(dLon/2)

	return 2 * earthRadiusKM * math.Asin(math.Min(1, math.Sqrt(a)))
}
