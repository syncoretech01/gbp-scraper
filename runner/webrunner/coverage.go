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

	// cellYield records the cumulative net-new of every ZIP cell the plan
	// has covered, which is what the marginal-unique-yield expansion gate
	// judges. See coverage_rank.go.
	cellYield map[string]coverageCellYield
	// expansionProbes and expansionProbeNetNew measure what neighbour
	// expansions have actually paid this run, so the ranking can price
	// further ones from evidence rather than from a prior.
	expansionProbes      int
	expansionProbeNetNew int64

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

	// Only the operator's own plan queries are evidence about the plan.
	//
	// Expansions and refinements are engine-generated probes into ground
	// nobody asked for, and a probe that finds nothing says something about
	// that neighbour cell, not about the queries still queued. Counting them
	// let three empty neighbour probes fill the window and skip a productive
	// plan query: measured in a dense metro, that cost 14 of 67 businesses.
	// Their yield still governs further expansion through the budget and the
	// net-new gate, which is where it belongs.
	if succeeded && task.Origin == "" {
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

	// Every successful attempt is yield evidence, whether it was a plan
	// query or an engine probe: the expansion gate and the work ranking are
	// both priced from it.
	engine.recordYieldLocked(task, checkpoint)

	// Refinements and neighbour expansions draw on one budget, so they are
	// ranked against each other by expected marginal unique yield rather
	// than funded in a fixed order. See planAppendedWorkLocked.
	engine.planAppendedWorkLocked(task, checkpoint, &decision)

	return decision
}

// expansionCandidatesLocked offers the nearest unexplored neighbours of a
// productive GBP-shaped cell as fundable work, nearest first. It builds
// nothing durable: the ranking in planAppendedWorkLocked decides which
// candidates are actually paid for out of the shared budget. Callers hold
// engine.mu.
func (engine *coverageEngine) expansionCandidatesLocked(
	task web.JobTask,
	checkpoint web.JobTaskCheckpoint,
	decision *coverageDecision,
) []coverageWorkCandidate {
	synonym, parentZIP, ok := web.SplitGBPQuery(task.Query)
	if !ok {
		return nil
	}

	if !engine.expansionWarrantedLocked(parentZIP) {
		return nil
	}

	parent, found := engine.zipAreaLocked(parentZIP)
	if !found {
		return nil
	}

	// The parent ZIP itself is covered by definition, even when its query
	// was not parsed at seed time.
	engine.known[parentZIP] = struct{}{}

	// One neighbourhood per parent ZIP, however many synonyms cross it. The
	// claim is only spent when a neighbour is actually funded, so a parent
	// whose candidates lose the ranking keeps its chance.
	if engine.parentExpansionSpentLocked(parentZIP) {
		return nil
	}

	type neighbour struct {
		area     prospect.ZIPArea
		distance float64
	}

	neighbours := make([]neighbour, 0, 64)

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

		neighbours = append(neighbours, neighbour{
			area:     area,
			distance: haversineKM(parent.Latitude, parent.Longitude, area.Latitude, area.Longitude),
		})
	}

	if len(neighbours) == 0 {
		return nil
	}

	sort.Slice(neighbours, func(a, b int) bool {
		if neighbours[a].distance == neighbours[b].distance {
			return neighbours[a].area.ZIP < neighbours[b].area.ZIP
		}

		return neighbours[a].distance < neighbours[b].distance
	})

	decision.parentZIP = parentZIP

	score := engine.expectedExpansionYieldLocked(coverageNetNewRows(checkpoint))
	separation := engine.zipSeparationFloorLocked()
	chosen := make([]prospect.ZIPArea, 0, coverageExpansionBatch)
	candidates := make([]coverageWorkCandidate, 0, coverageExpansionBatch)

	// Neighbours are re-checked as the batch fills: two of them can each
	// clear the parent yet nearly coincide with each other.
	for _, offered := range neighbours {
		if len(candidates) >= coverageExpansionBatch {
			break
		}

		if coverageZIPTooClose(chosen, offered.area, separation) {
			continue
		}

		area := offered.area
		chosen = append(chosen, area)

		candidates = append(candidates, coverageWorkCandidate{
			kind:  coverageWorkExpansion,
			score: score,
			build: func(sequence int) web.JobTaskDefinition {
				return expansionTaskDefinition(engine.jobID, synonym, parentZIP, area, sequence)
			},
			onFund: func() {
				engine.known[area.ZIP] = struct{}{}
				engine.claimParentExpansionLocked(parentZIP)
			},
		})
	}

	return candidates
}

// coverageZIPTooClose reports whether a candidate sits inside the separation
// floor of any ZIP already picked for this batch.
func coverageZIPTooClose(picked []prospect.ZIPArea, candidate prospect.ZIPArea, separation float64) bool {
	for _, area := range picked {
		if haversineKM(area.Latitude, area.Longitude, candidate.Latitude, candidate.Longitude) < separation {
			return true
		}
	}

	return false
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

	// Fast mode and browser mode need different seed types: a fast-mode run
	// searches through structured lat/lon/radius parameters, a browser run
	// drives a map viewport. Building a browser seed inside a fast-mode job
	// silently returned nothing, which is why expansion measured zero new
	// businesses in every market. CreateSeedJobs owns that branching for the
	// plan, so expansion reuses it rather than restating half of it.
	seeds, seedErr := runner.CreateSeedJobs(
		job.Data.FastMode,
		job.Data.Lang,
		strings.NewReader(payload.Query),
		job.Data.Depth,
		job.Data.Email,
		payload.Coordinates,
		resolveSeedZoom(job.Data.Zoom, payload.Zoom),
		float64(job.Data.Radius),
		dedup,
		exitMonitor,
		extraReviews,
	)
	if seedErr != nil {
		return nil, fmt.Errorf("build expansion seed for task %q: %w", task.Key, seedErr)
	}

	if len(seeds) != 1 {
		return nil, fmt.Errorf("expansion task %q produced %d seeds, want exactly one", task.Key, len(seeds))
	}

	seed := seeds[0]
	// The durable task key is the seed identity, so a restart re-runs the
	// same expansion instead of a fresh random one.
	switch typed := seed.(type) {
	case *gmaps.GmapJob:
		typed.ID = seedID
	case *gmaps.SearchJob:
		typed.ID = seedID
	}

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
