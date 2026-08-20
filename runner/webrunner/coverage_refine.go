package webrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/exiter"
	"github.com/gosom/google-maps-scraper/web"
	"github.com/gosom/google-maps-scraper/web/prospect"
)

// Discovery blind-spot constants.
//
// The scraper reads listings out of the Maps result feed by scrolling it:
// gmaps.GmapJob.BrowserActions calls scroll(ctx, page, MaxDepth, …), whose
// loop runs at most MaxDepth times and stops early once the feed's height
// stops growing. Each scroll appends one lazily loaded page of listings, and
// the feed itself never renders more than a fixed ceiling of them however
// far it is scrolled. A query that comes back with as many listings as that
// budget allows therefore says nothing about how many businesses exist in
// the cell — only that the platform stopped handing them over.
//
// gmaps is deliberately not modified to report this: scroll() already
// computes how many iterations it used and BrowserActions discards the
// value, and the exit monitor's counters are process-global rather than
// per-task, so parallel workers cannot attribute them. The signal below is
// therefore derived from the only per-task evidence that is durably
// recorded: the merged row counters of the task's own result file.
const (
	// gmapsFeedPageSize is how many listings one scroll of the Maps result
	// feed appends.
	gmapsFeedPageSize = 20
	// gmapsFeedResultCeiling is the largest number of listings the feed
	// renders for a single query no matter how deep it is scrolled.
	gmapsFeedResultCeiling = 120
	// maximumMapsZoom is the tightest zoom level a Maps search URL accepts;
	// it matches the bound JobData.Validate enforces.
	maximumMapsZoom = 21
	// coverageRefinementZoomStep is how much tighter a refinement views the
	// same cell. Two levels roughly quarters the viewport, which pulls
	// listings that ranked below the feed cut-off into it.
	coverageRefinementZoomStep = 2
	// coverageMaxRefinementsPerTask caps how many refinements one truncated
	// task may earn. One bounded second look per cell keeps the shared
	// expansion budget available for unexplored ground.
	coverageMaxRefinementsPerTask = 1
	// coverageMinimumZIPSeparationKM is how far apart two ZIP centroids must
	// be before their queries are treated as covering different ground. It
	// is a deliberately conservative floor: in the embedded US dataset
	// 53,688 same-state ZIP pairs sit closer than this to each other, some
	// of them (00501/00544, for instance) sharing a centroid exactly, so
	// querying both spends budget twice on one neighbourhood.
	coverageMinimumZIPSeparationKM = 1.0
	// metresPerKM converts JobData.Radius, which is configured in metres.
	metresPerKM = 1000.0
)

// coverageBlindspotState is the per-run bookkeeping of the discovery
// blind-spot guards: which cells were already given a second look and which
// parent ZIPs already spent budget on their neighbourhood.
//
// It lives inside coverageEngine and is only touched with engine.mu held.
type coverageBlindspotState struct {
	// planZoom is the job's configured zoom, needed to derive a tighter one
	// for a refinement.
	planZoom int
	// searchRadiusKM is the job's configured search radius, which sets how
	// far apart two ZIP centroids must be to cover different ground.
	searchRadiusKM float64
	// refinedTasks counts refinements earned per parent task key.
	refinedTasks map[string]int
	// expandedParents records the parent ZIPs that already seeded neighbour
	// expansions this run.
	expandedParents map[string]struct{}
}

// withPlanZoom records the job's configured zoom on the engine so a
// refinement can request a tighter view of the same cell. It returns the
// engine so construction stays a single expression.
func (engine *coverageEngine) withPlanZoom(zoom int) *coverageEngine {
	engine.mu.Lock()
	defer engine.mu.Unlock()

	engine.blindspot.planZoom = zoom

	return engine
}

// withSearchRadius records the job's configured search radius in metres so
// neighbour selection can tell genuinely new ground from a cell the plan
// already swept. It returns the engine so construction stays a single
// expression.
func (engine *coverageEngine) withSearchRadius(metres int) *coverageEngine {
	engine.mu.Lock()
	defer engine.mu.Unlock()

	engine.blindspot.searchRadiusKM = float64(metres) / metresPerKM

	return engine
}

// zipSeparationFloorLocked is how far apart two ZIP centroids must be before
// their queries are treated as covering different ground.
//
// A search sweeps a radius around its centre, so two centres closer together
// than that radius return largely the same listings however different their
// ZIP codes look. Measured in a dense metro: expanding from ZIP 94110 into
// 94114, 94107 and 94131 — 1.78, 2.03 and 2.13 km away against a 10 km
// radius — produced three completed tasks and zero rows, because the shared
// in-run deduper had already seen every result. The floor therefore tracks
// the configured radius, falling back to a conservative fixed minimum when a
// job did not set one. Callers hold engine.mu.
func (engine *coverageEngine) zipSeparationFloorLocked() float64 {
	if engine.blindspot.searchRadiusKM > coverageMinimumZIPSeparationKM {
		return engine.blindspot.searchRadiusKM
	}

	return coverageMinimumZIPSeparationKM
}

// effectiveQueryResultCap is the largest number of listings one query can
// yield at the given depth: one feed page per scroll, never more than the
// feed's own ceiling. Depth below one is treated as one, matching the bound
// JobData.Validate enforces.
func effectiveQueryResultCap(depth int) int {
	if depth < 1 {
		depth = 1
	}

	resultCap := depth * gmapsFeedPageSize
	if resultCap > gmapsFeedResultCeiling {
		resultCap = gmapsFeedResultCeiling
	}

	return resultCap
}

// coverageTaskYield estimates how many distinct listings one task's result
// file contained. RowsAdded counts the rows the merge accepted from the run
// file and DuplicatesSkipped the rows it collapsed into an identity already
// written, so together they account for every row the query produced.
//
// Note that rows the task re-found from an earlier task land in RowsAdded
// (the merge drops the older copy and counts it under RowsReplaced), which
// is exactly what a yield estimate wants: the query's own harvest, not how
// much of it was new to the job.
func coverageTaskYield(checkpoint web.JobTaskCheckpoint) int64 {
	return checkpoint.RowsAdded + checkpoint.DuplicatesSkipped
}

// coverageNetNewRows is how many businesses a finished task contributed that
// the job had not already collected.
//
// RowsAdded on its own cannot answer that: when a task re-finds a business an
// earlier task already committed, the merge drops the older copy (counting it
// under RowsReplaced) and writes the fresh one, which lands in RowsAdded. A
// query that found nothing new therefore still reports a full RowsAdded, and
// DuplicatesSkipped stays at zero because it only counts collisions inside
// one result file. Expansion budget must follow genuinely new ground, so the
// gate reads the difference.
func coverageNetNewRows(checkpoint web.JobTaskCheckpoint) int64 {
	// One run row can supersede several stored rows, because a business is
	// matched on any of its identities.
	if checkpoint.RowsReplaced >= checkpoint.RowsAdded {
		return 0
	}

	return checkpoint.RowsAdded - checkpoint.RowsReplaced
}

// markCoverageTruncation records on the checkpoint whether the task's own
// result set reached the effective per-query cap for the configured depth,
// which means real businesses in that cell were very likely never rendered.
//
// A nil engine leaves the checkpoint byte-for-byte as the historical runner
// produced it, so a job without a coverage block keeps exactly today's
// durable payload.
func markCoverageTruncation(engine *coverageEngine, checkpoint *web.JobTaskCheckpoint, depth int) {
	if engine == nil || checkpoint == nil {
		return
	}

	resultCap := effectiveQueryResultCap(depth)

	checkpoint.TruncationCap = resultCap
	checkpoint.Truncated = coverageTaskYield(*checkpoint) >= int64(resultCap)
}

// isCoverageAppendedOrigin reports whether an origin marks a task the
// coverage engine appended mid-run, whose seed must be rebuilt from its
// durable payload rather than from the up-front plan.
func isCoverageAppendedOrigin(origin string) bool {
	return strings.HasPrefix(origin, web.CoverageExpansionOriginPrefix) ||
		strings.HasPrefix(origin, web.CoverageRefinementOriginPrefix)
}

// resolveSeedZoom prefers the zoom an appended task recorded in its durable
// payload, falling back to the job's configured zoom for payloads written
// before refinements existed.
func resolveSeedZoom(planZoom, payloadZoom int) int {
	if payloadZoom > 0 {
		return payloadZoom
	}

	return planZoom
}

// refinementZoom is the tighter zoom a refinement of the given plan uses. It
// reports zero when no tighter view is available, in which case the engine
// has no cheaper correct refinement to offer and declines to spend budget.
func refinementZoom(planZoom int) int {
	if planZoom < 1 || planZoom >= maximumMapsZoom {
		return 0
	}

	zoom := planZoom + coverageRefinementZoomStep
	if zoom > maximumMapsZoom {
		zoom = maximumMapsZoom
	}

	return zoom
}

// zipAreaLocked looks one ZIP up in the engine's dataset index, building the
// index on first use. Callers hold engine.mu.
func (engine *coverageEngine) zipAreaLocked(zip string) (prospect.ZIPArea, bool) {
	if engine.zipIndex == nil {
		areas := engine.zipAreas()
		engine.zipIndex = make(map[string]prospect.ZIPArea, len(areas))

		for _, area := range areas {
			engine.zipIndex[area.ZIP] = area
		}
	}

	area, found := engine.zipIndex[zip]

	return area, found
}

// overlapsCoveredZIPLocked reports whether a candidate ZIP sits so close to
// a ZIP the plan already covers that both queries would search the same
// ground. Skipping such a candidate leaves the shared budget for genuinely
// unexplored ZIPs. Callers hold engine.mu.
func (engine *coverageEngine) overlapsCoveredZIPLocked(candidate prospect.ZIPArea) bool {
	for zip := range engine.known {
		if zip == candidate.ZIP {
			return true
		}

		covered, found := engine.zipAreaLocked(zip)
		if !found {
			continue
		}

		distance := haversineKM(
			covered.Latitude, covered.Longitude, candidate.Latitude, candidate.Longitude,
		)
		if distance < engine.zipSeparationFloorLocked() {
			return true
		}
	}

	return false
}

// claimParentExpansionLocked reports whether this parent ZIP may still seed
// neighbour expansions, and records the claim.
//
// A plan is a synonym x ZIP cross product, so one productive ZIP completes
// once per synonym. Without this guard each of those completions spends its
// own slice of the shared budget on the same neighbourhood, which is the
// duplicate work the coverage audit set out to remove. Callers hold
// engine.mu.
func (engine *coverageEngine) claimParentExpansionLocked(parentZIP string) bool {
	if engine.blindspot.expandedParents == nil {
		engine.blindspot.expandedParents = make(map[string]struct{})
	}

	if _, spent := engine.blindspot.expandedParents[parentZIP]; spent {
		return false
	}

	engine.blindspot.expandedParents[parentZIP] = struct{}{}

	return true
}

// refineLocked gives one truncated cell a bounded second look before any
// budget goes to its neighbours: a query whose feed was cut off has unseen
// businesses inside the cell, so widening the search first would compound
// the blind spot.
//
// The refinement re-queries the same ZIP at a tighter zoom, centred on the
// ZIP's own centroid. That is the cheapest correct refinement the current
// engine supports: it needs no change to gmaps, adds no concurrency, and
// spends the same MaxExpansions budget neighbour expansions draw on.
// Callers hold engine.mu.
func (engine *coverageEngine) refineLocked(
	task web.JobTask,
	checkpoint web.JobTaskCheckpoint,
	decision *coverageDecision,
) {
	if !checkpoint.Truncated {
		return
	}

	if engine.options.MaxExpansions <= 0 || engine.expansionsAdded >= engine.options.MaxExpansions {
		return
	}

	// One bounded pass per cell: a refinement is never itself refined, so a
	// stubbornly capped cell cannot start a chain.
	if strings.HasPrefix(task.Origin, web.CoverageRefinementOriginPrefix) {
		return
	}

	if engine.blindspot.refinedTasks == nil {
		engine.blindspot.refinedTasks = make(map[string]int)
	}

	if engine.blindspot.refinedTasks[task.Key] >= coverageMaxRefinementsPerTask {
		return
	}

	synonym, zip, ok := web.SplitGBPQuery(task.Query)
	if !ok {
		return
	}

	zoom := refinementZoom(engine.blindspot.planZoom)
	if zoom == 0 {
		return
	}

	area, found := engine.zipAreaLocked(zip)
	if !found {
		return
	}

	definition := refinementTaskDefinition(engine.jobID, synonym, area, zoom, engine.nextSequence)

	engine.nextSequence++
	engine.expansionsAdded++
	engine.blindspot.refinedTasks[task.Key]++

	decision.refinements = append(decision.refinements, definition)
}

// refinementTaskDefinition builds the durable description of one refinement
// task. It reuses the appended-task payload so a worker rebuilds either kind
// of appended seed through the same restart-safe path.
func refinementTaskDefinition(
	jobID, synonym string,
	area prospect.ZIPArea,
	zoom, sequence int,
) web.JobTaskDefinition {
	query := strings.Join(strings.Fields(fmt.Sprintf(
		"%s in %s %s %s", synonym, area.City, strings.ToUpper(area.State), area.ZIP,
	)), " ")

	// The zoom is part of the identity: the refinement searches the same
	// text as its parent and is only a different task because it looks at
	// the cell more closely.
	identity := strings.Join([]string{
		"coverage-refinement", jobID, query, fmt.Sprintf("%dz", zoom),
	}, "\x1f")
	key := "ref-" + uuid.NewSHA1(uuid.NameSpaceURL, []byte(identity)).String()

	payload, err := json.Marshal(expansionTaskPayload{
		SeedID:      key,
		Sequence:    sequence,
		Query:       query,
		Coordinates: fmt.Sprintf("%.6f,%.6f", area.Latitude, area.Longitude),
		ZIP:         area.ZIP,
		ParentZIP:   area.ZIP,
		Expansion:   true,
		Refinement:  true,
		Zoom:        zoom,
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
		Origin:   web.CoverageRefinementOriginPrefix + area.ZIP,
		Priority: 1,
	}
}

// appendCoverageRefinements performs the durable side of a refinement
// decision: it appends the tasks, tells the exit monitor to expect them, and
// records the evidence. It is a no-op when the decision earned none, so a
// run without a coverage engine reaches exactly today's behaviour.
func (w *webrunner) appendCoverageRefinements(
	run *taskPoolRun,
	checkpoint web.JobTaskCheckpoint,
	decision coverageDecision,
	exitMonitor exiter.Exiter,
) {
	if len(decision.refinements) == 0 {
		return
	}

	job := run.job

	maximumAttempts := 1
	if job.Data.RetryConfigured {
		maximumAttempts = max(1, job.Data.RetryCount+1)
	}

	inserted, appendErr := w.svc.AppendJobTasks(
		context.Background(), job.ID, decision.refinements, maximumAttempts,
	)
	if appendErr != nil {
		_ = w.svc.RecordJobWorkerEvent(
			context.Background(), job.ID, "coverage-refined", "warning",
			"Coverage refinement tasks could not be appended to the plan",
			map[string]any{"error": appendErr.Error()},
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

	origins := make([]string, 0, len(inserted))
	for _, appended := range inserted {
		origins = append(origins, appended.Origin)
	}

	_ = w.svc.RecordJobWorkerEvent(
		context.Background(), job.ID, "coverage-refined", "information",
		fmt.Sprintf(
			"Coverage refined: %d task(s) re-cover a cell whose %d result(s) reached the depth cap of %d",
			len(inserted), coverageTaskYield(checkpoint), checkpoint.TruncationCap,
		),
		map[string]any{
			"appended":       len(inserted),
			"origins":        origins,
			"result_cap":     checkpoint.TruncationCap,
			"observed_rows":  coverageTaskYield(checkpoint),
			"refinement_for": decision.refinements[0].Query,
		},
	)
}
