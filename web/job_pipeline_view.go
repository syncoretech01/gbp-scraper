package web

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
	"sort"
	"time"
)

// notReported is the single phrase the pipeline uses for evidence the worker
// has not published yet. Keeping it in one place is what lets app-monitor.js
// style every such value as an empty state instead of as data.
const notReported = "—"

// Metric groups let one page lift a coherent set of stage metrics out and
// present it on its own. The template matches on these constants, never on the
// human wording, so a label can be improved without breaking a layout.
const (
	// pipelineGroupGeography is the distance distribution of the collected
	// businesses relative to the configured search area.
	pipelineGroupGeography = "geography"
	// pipelineGroupRunCount is the run counting vocabulary: observations,
	// repeated observations, unique businesses, entity merges.
	pipelineGroupRunCount = "run-count"
	// pipelineGroupGeographyFilter carries the one non-destructive action the
	// geography panel offers. Its Value is a relative URL rather than a
	// number, which is why it is a group of its own: the template renders it
	// as a link and never as a metric.
	pipelineGroupGeographyFilter = "geography-filter"
)

// jobPipelineMetric is one named measurement inside a pipeline stage. Label is
// the specification's own wording so an operator can match the console to the
// documentation; Value is always a rendered string, because a stage that has
// not run reports a phrase rather than a misleading zero.
type jobPipelineMetric struct {
	Label string
	Value string
	// Group tags a metric so a page can lift one coherent set out of a stage
	// and present it on its own — the geographic spread, for instance — without
	// the template having to match on wording that may be reworded later.
	Group string
	// Note is the one-line plain-language gloss shown under the value. It
	// exists because several of these metrics are only honest with their
	// definition attached.
	Note string
}

// jobPipelineInput is everything the eight-stage view is derived from. It is a
// struct rather than a long parameter list so a new stage metric can take a new
// source without changing every call site.
type jobPipelineInput struct {
	Job        Job
	Runtime    JobRuntime
	Stats      ResultStats
	Execution  JobExecutionSnapshot
	Facts      JobPipelineFacts
	HasFacts   bool
	RawRecords int64
	// Coverage is the durable per-task checkpoint sum for this job. It is the
	// only evidence that distinguishes a Maps observation from a business, so
	// the deduplicating stage reports "not reported yet" without it rather
	// than presenting the business count under an observation label.
	Coverage CoverageTotals
}

// runObservations reduces the input to the run counting vocabulary defined in
// web/run_metrics.go. Every consumer of that vocabulary goes through here so
// the monitor cannot drift back into calling one thing by three names.
func (input jobPipelineInput) runObservations() RunObservations {
	// The result summary already carries the checkpoint totals for this job.
	// Coverage is the override for a caller that has a fresher report in hand
	// — a live run polls it far more often than the file is re-summarized.
	observations := input.Stats.Run
	if fresher := NewRunObservations(input.Coverage); fresher.Available {
		observations = fresher
	}

	if input.Stats.UniqueBusinesses > 0 {
		observations = observations.WithUniqueBusinesses(int64(input.Stats.UniqueBusinesses))
	}
	if input.HasFacts {
		observations = observations.WithEntityMerges(input.Facts.Merged)
	}

	return observations
}

// pipelineStageOrder is the canonical eight-stage pipeline. The order is the
// specification's and is also the order jobruntime.Stage advances through, so
// the index doubles as the progress position.
var pipelineStageOrder = []struct {
	stage jobruntime.Stage
	label string
}{
	{jobruntime.StagePreparingQueries, "Preparing queries"},
	{jobruntime.StageGeneratingGrid, "Generating grid"},
	{jobruntime.StageSearchingMaps, "Searching Maps"},
	{jobruntime.StageExtractingDetails, "Extracting details"},
	{jobruntime.StageCrawlingWebsites, "Crawling websites"},
	{jobruntime.StageExtractingContacts, "Extracting contacts"},
	{jobruntime.StageDeduplicating, "Deduplicating"},
	{jobruntime.StageSavingExporting, "Saving/exporting"},
}

// buildPipeline renders the eight stages with the per-stage metrics the
// specification names for each one. Every value comes from durable evidence —
// the task plan, the redacted event log, the committed CSV, or the worker's
// published progress — so a stage never claims work it cannot substantiate.
func buildPipeline(input jobPipelineInput) []jobPipelineStep {
	active := 0
	for index, stage := range pipelineStageOrder {
		if stage.stage == input.Runtime.Stage {
			active = index

			break
		}
	}

	metrics := [][]jobPipelineMetric{
		preparingQueryMetrics(input),
		generatingGridMetrics(input),
		searchingMapsMetrics(input),
		extractingDetailMetrics(input),
		crawlingWebsiteMetrics(input),
		extractingContactMetrics(input),
		deduplicatingMetrics(input),
		savingExportingMetrics(input),
	}

	steps := make([]jobPipelineStep, 0, len(pipelineStageOrder))
	for index := range pipelineStageOrder {
		state, detail := pipelineStageState(input.Runtime, index, active)
		// The job's legacy status is allowed to finish before website audits
		// do (changing that would alter pause/stop/restart semantics), so the
		// crawl stage says so instead of claiming completion it has not earned.
		if pipelineStageOrder[index].stage == jobruntime.StageCrawlingWebsites &&
			input.HasFacts && input.Facts.EnrichmentTasksTotal > 0 && !input.Facts.EnrichmentComplete {
			state, detail = "active", fmt.Sprintf("Website audits still running (%d queued)", input.Facts.EnrichmentPending())
		}
		steps = append(steps, jobPipelineStep{
			Order:   index,
			Label:   pipelineStageOrder[index].label,
			Detail:  detail,
			State:   state,
			Metrics: metrics[index],
		})
	}

	return steps
}

func pipelineStageState(runtime JobRuntime, index, active int) (string, string) {
	switch {
	case runtime.State == jobruntime.StateCompleted:
		return "complete", "Completed"
	case index < active:
		return "complete", "Completed"
	case index == active && runtime.State == jobruntime.StatePaused:
		return "paused", "Paused"
	case index == active && runtime.State == jobruntime.StateFailed:
		return "failed", "Stopped with an error"
	case index == active && runtime.State.Active():
		return "active", currentTaskLabel(runtime)
	case index == active && runtime.State == jobruntime.StatePartial:
		return "partial", "Stopped with partial results"
	default:
		return "pending", "Waiting"
	}
}

// preparingQueryMetrics reports keyword expansion, validation, duplicate
// removal, and the generated search count. Expansion is measured against the
// configured keyword list: the adaptive coverage engine appends tasks, so a
// generated count above the configured one is the expansion itself.
func preparingQueryMetrics(input jobPipelineInput) []jobPipelineMetric {
	configured := len(input.Job.Data.Keywords)
	valid := 0
	unique := map[string]struct{}{}
	for _, keyword := range input.Job.Data.Keywords {
		trimmed := strings.TrimSpace(keyword)
		if trimmed == "" {
			continue
		}
		valid++
		unique[strings.ToLower(trimmed)] = struct{}{}
	}

	generated := notReported
	if input.HasFacts && input.Facts.QueriesPlanned > 0 {
		generated = strconv.FormatInt(input.Facts.QueriesPlanned, 10)
	} else if input.Execution.Tasks.Total > 0 {
		generated = strconv.FormatInt(input.Execution.Tasks.Total, 10)
	}

	return []jobPipelineMetric{
		{Label: "Keywords configured", Value: strconv.Itoa(configured)},
		{Label: "Passed validation", Value: fmt.Sprintf("%d of %d", valid, configured)},
		{Label: "Duplicates removed", Value: strconv.Itoa(valid - len(unique))},
		{Label: "Searches generated", Value: generated},
	}
}

// generatingGridMetrics reports cells created, cells excluded, the geographic
// coverage the cells were cut from, and the task estimate.
func generatingGridMetrics(input jobPipelineInput) []jobPipelineMetric {
	created := notReported
	if input.HasFacts && input.Facts.CellsPlanned > 0 {
		created = strconv.FormatInt(input.Facts.CellsPlanned, 10)
	}

	excluded := "0"
	if geometry, err := ParseMapGeometry([]byte(input.Job.Data.AreaGeoJSON)); err == nil {
		excluded = strconv.Itoa(len(geometry.ExcludedCellIDs()))
	} else if strings.TrimSpace(input.Job.Data.AreaGeoJSON) == "" {
		excluded = "no saved area"
	}

	estimate := notReported
	if total := max(input.Execution.Tasks.Total, input.Runtime.TotalTasks); total > 0 {
		estimate = strconv.FormatInt(total, 10)
	}

	metrics := []jobPipelineMetric{
		{Label: "Cells created", Value: created},
		{Label: "Cells excluded", Value: excluded},
		{Label: "Geographic coverage", Value: locationSummary(input.Job.Data)},
		{Label: "Task estimate", Value: estimate},
	}

	return append(metrics, geographyMetrics(input)...)
}

// geographyMetrics states where the collected businesses actually landed
// relative to the area the job was configured to search.
//
// Google does not honour a radius: a grid-cell query is a viewport hint, and
// Maps answers a sparse category by widening the search rather than returning
// a short list. A run therefore routinely keeps businesses far outside the
// radius the operator typed, and those businesses are real discovery that must
// not be thrown away. The honest response is to measure the spread and name
// it, which is what these metrics do. Nothing here removes a row.
func geographyMetrics(input jobPipelineInput) []jobPipelineMetric {
	geography := input.Stats.Geography
	if !geography.Available || geography.Measured == 0 {
		return nil
	}

	area := NewJobSearchArea(input.Job.Data)
	radius := humanDistance(geography.RadiusMeters)
	metrics := make([]jobPipelineMetric, 0, 8)

	// A job configured without a radius has no circle to be inside of, and a
	// tile reading "inside the 0 m radius" would be an invented boundary.
	if geography.RadiusMeters > 0 {
		metrics = append(metrics, jobPipelineMetric{
			Group: pipelineGroupGeography,
			Label: "Inside the " + radius + " radius",
			Value: fmt.Sprintf("%d of %d", geography.InsideRadius, geography.Measured),
			Note: fmt.Sprintf("%.1f%% of the businesses this run kept",
				sharePercent(geography.InsideRadius, geography.Measured)),
		})
	}

	// The corner band only exists for a gridded job with a radius: the planner
	// cuts cells from the square that encloses the circle, so its corner cells
	// legitimately reach past the radius. Without one or the other there is no
	// such band, and a metric explaining one would describe machinery that
	// never ran.
	if area.HasGrid && geography.RadiusMeters > 0 {
		metrics = append(metrics, jobPipelineMetric{
			Group: pipelineGroupGeography,
			Label: "Past the radius, inside the searched grid",
			Value: strconv.Itoa(geography.InsidePlanned),
			Note:  "the grid is cut from the square around the circle, so its corner cells reach past " + radius,
		})
	}

	metrics = append(metrics, jobPipelineMetric{
		Group: pipelineGroupGeography,
		Label: "Outside the area this run searched",
		Value: fmt.Sprintf("%d (%.1f%%)", geography.OutsidePlanned, geography.OutsidePlannedPercent()),
		Note:  "Maps returned these on its own; they are real businesses and they are kept",
	}, jobPipelineMetric{
		Group: pipelineGroupGeography,
		Label: "Median distance from the centre",
		Value: humanDistance(geography.MedianMeters),
		Note:  "planned searches reached " + humanDistance(geography.PlannedReachMeters) + " at most",
	})

	farthest := jobPipelineMetric{
		Group: pipelineGroupGeography,
		Label: "Farthest business kept",
		Value: humanDistance(geography.MaxMeters),
		Note:  "no search pointed further than " + humanDistance(geography.PlannedReachMeters),
	}
	if geography.FarthestName != "" {
		farthest.Note = geography.FarthestName
	}
	metrics = append(metrics, farthest)

	// The spillover measurement is what makes the outside-the-area count
	// attributable. Distance from the configured centre alone cannot say
	// whether the grid was cut wrong or the platform widened the query; the
	// distance from the cell that actually asked can.
	if spillover := geography.Spillover; spillover.Available && spillover.CellReachMeters > 0 {
		metrics = append(metrics, jobPipelineMetric{
			Group: pipelineGroupGeography,
			Label: "Returned from outside the cell that searched",
			Value: fmt.Sprintf("%d of %d (%.1f%%)",
				spillover.Beyond, spillover.Measured, spillover.BeyondPercent()),
			Note: "median " + humanDistance(spillover.MedianMeters) + " from the searching cell, up to " +
				humanDistance(spillover.MaxMeters) + "; the cells themselves only reach " +
				humanDistance(spillover.CellReachMeters),
		})
	}

	if geography.WithoutCoordinates > 0 {
		metrics = append(metrics, jobPipelineMetric{
			Group: pipelineGroupGeography,
			Label: "Stored without coordinates",
			Value: strconv.Itoa(geography.WithoutCoordinates),
			Note:  "no position was recorded, so these are not measured above",
		})
	}

	if action, ok := geographyFilterAction(input.Job.ID, geography); ok {
		metrics = append(metrics, action)
	}

	return metrics
}

// geographyFilterAction builds the optional post-collection geographic filter.
//
// It is a link into the stored-results view with the job's own centre and
// radius pre-filled, which is deliberately the only kind of geographic
// filtering this application performs: it narrows what is displayed and
// exported and touches no stored row, so the discovery a run paid for survives
// even when an operator only wants the part inside the circle. Clearing the
// filter brings every business back.
func geographyFilterAction(jobID string, geography ResultGeography) (jobPipelineMetric, bool) {
	if geography.RadiusFilterValue == "" || geography.OutsideRadius() == 0 {
		return jobPipelineMetric{}, false
	}

	values := url.Values{}
	values.Set("job_id", jobID)
	values.Set("filter_field", "distance")
	values.Set("filter_operator", "within")
	values.Set("filter_value", geography.RadiusFilterValue)

	// The label deliberately quotes no count. The counts above are great-circle
	// measurements; the stored-results filter this link opens uses a flat-earth
	// approximation and can disagree by a business sitting on the line. A
	// button promising "189" that lands on a list of 188 is worse than a button
	// that promises a filter and delivers one.
	return jobPipelineMetric{
		Group: pipelineGroupGeographyFilter,
		Label: "Show only the businesses inside " + humanDistance(geography.RadiusMeters),
		Value: "/app/results?" + values.Encode(),
		Note:  "Filters the saved results. Every business stays in the workspace and comes back when the filter is cleared.",
	}, true
}

// searchingMapsMetrics reports the current query, coordinates, cell, results
// found, speed, and block rate.
func searchingMapsMetrics(input jobPipelineInput) []jobPipelineMetric {
	coordinates := notReported
	if latitude := strings.TrimSpace(input.Job.Data.Lat); latitude != "" {
		coordinates = latitude + ", " + strings.TrimSpace(input.Job.Data.Lon)
	}

	blockRate := notReported
	if input.HasFacts {
		blockRate = fmt.Sprintf("%.1f%% (%d blocks)", input.Facts.BlockRatePercent(), input.Facts.BlockEvents())
	}

	return []jobPipelineMetric{
		{Label: "Current query", Value: valueOrNotReported(input.Execution.Progress.CurrentQuery)},
		{Label: "Coordinates", Value: coordinates},
		{Label: "Grid cell", Value: valueOrNotReported(input.Execution.Progress.CurrentCell)},
		{Label: "Results found", Value: strconv.FormatInt(input.RawRecords, 10)},
		{Label: "Speed", Value: fmt.Sprintf("%.1f places/min", input.Execution.Progress.PlacesPerMinute)},
		{Label: "Block rate", Value: blockRate},
	}
}

// extractingDetailMetrics reports listings opened, fields parsed, retries, and
// browser health.
func extractingDetailMetrics(input jobPipelineInput) []jobPipelineMetric {
	retries := input.Execution.Tasks.Retries
	if input.HasFacts && input.Facts.Retries > retries {
		retries = input.Facts.Retries
	}

	browserHealth := notReported
	if input.HasFacts {
		failures := input.Facts.EventsByType["browser-failure"]
		switch {
		case failures == 0 && input.Execution.Progress.BrowserCount > 0:
			browserHealth = fmt.Sprintf("%d browser(s), no failures", input.Execution.Progress.BrowserCount)
		case failures == 0:
			browserHealth = "no failures recorded"
		default:
			browserHealth = fmt.Sprintf("%d browser failure(s) recorded", failures)
		}
	}

	parseFailures := notReported
	if input.HasFacts {
		parseFailures = strconv.FormatInt(input.Facts.EventsByType["parsing-failure"], 10)
	}

	return []jobPipelineMetric{
		{Label: "Listings opened", Value: strconv.FormatInt(input.RawRecords, 10)},
		{Label: "Detail rows parsed", Value: strconv.Itoa(input.Stats.Rows)},
		{Label: "Parsing failures", Value: parseFailures},
		{Label: "Retries", Value: strconv.FormatInt(retries, 10)},
		{Label: "Browser health", Value: browserHealth},
	}
}

// crawlingWebsiteMetrics reports the current domain, pages visited, the last
// HTTP status, and the average response time.
func crawlingWebsiteMetrics(input jobPipelineInput) []jobPipelineMetric {
	pages := notReported
	status := notReported
	response := notReported
	if input.HasFacts {
		pages = fmt.Sprintf("%d page(s) across %d site(s)", input.Facts.PagesChecked, input.Facts.WebsitesChecked)
		if input.Facts.DomainsChecked > 0 && input.Facts.DomainsChecked != input.Facts.WebsitesChecked {
			pages += fmt.Sprintf(" · %d domain(s)", input.Facts.DomainsChecked)
		}
		if input.Facts.LastHTTPStatus > 0 {
			status = strconv.FormatInt(input.Facts.LastHTTPStatus, 10)
		}
		if input.Facts.AverageResponseMS > 0 {
			response = fmt.Sprintf("%.0f ms", input.Facts.AverageResponseMS)
		}
	}

	return []jobPipelineMetric{
		{Label: "Current domain", Value: valueOrNotReported(input.Execution.Progress.CurrentDomain)},
		{Label: "Pages visited", Value: pages},
		{Label: "Last HTTP status", Value: status},
		{Label: "Average response time", Value: response},
		{Label: "Queue depth", Value: strconv.FormatInt(input.Execution.Progress.WebsiteQueue, 10)},
		{Label: "Website audit time", Value: durationMetricValue(input), Group: "timing",
			Note: "discovery · website audits · end to end"},
	}
}

// durationMetricValue separates discovery, enrichment and total wall time.
// The headline "Elapsed" is discovery only, which is how a Fast run that
// enriched 25 sites came to report 6 seconds for work that took minutes.
func durationMetricValue(input jobPipelineInput) string {
	if !input.HasFacts || input.Facts.TotalDurationMS <= 0 {
		return notReported
	}
	ms := func(v int64) string {
		return (time.Duration(v) * time.Millisecond).Round(time.Second).String()
	}
	return fmt.Sprintf("%s · %s · %s", ms(input.Facts.DiscoveryDurationMS),
		ms(input.Facts.EnrichmentDurationMS), ms(input.Facts.TotalDurationMS))
}

// extractingContactMetrics reports emails, phones, and social links found.
// Emails and phones come from the committed CSV so they hold even before the
// database import; social links only exist in the normalized tables, so they
// report honestly when that evidence is unavailable.
func extractingContactMetrics(input jobPipelineInput) []jobPipelineMetric {
	socials := notReported
	if input.HasFacts {
		socials = strconv.FormatInt(input.Facts.WithSocial, 10)
	}

	emails := int64(input.Stats.WithEmail)
	if input.HasFacts && input.Facts.WithEmail > emails {
		emails = input.Facts.WithEmail
	}
	phones := int64(input.Stats.WithPhone)
	if input.HasFacts && input.Facts.WithPhone > phones {
		phones = input.Facts.WithPhone
	}

	// "Emails discovered" used to be the number of BUSINESSES holding an
	// address, which the job cfe2d653 acceptance test read as eleven addresses
	// that never reached the export. The funnel is now spelled out: businesses,
	// distinct addresses, and what the extraction rules refused and why, so a
	// run can never imply addresses are available without saying which.
	metrics := []jobPipelineMetric{
		{Label: "Businesses with an email", Value: strconv.FormatInt(emails, 10)},
		{Label: "Phones discovered", Value: strconv.FormatInt(phones, 10)},
		{Label: "Social links discovered", Value: socials},
	}
	if input.HasFacts {
		metrics = append(metrics, jobPipelineMetric{
			Label: "Email addresses", Value: strconv.FormatInt(input.Facts.EmailAddresses, 10),
			Note: "distinct accepted addresses across those businesses",
		})
		if input.Facts.EmailsRejected > 0 {
			metrics = append(metrics, jobPipelineMetric{
				Label: "Candidates rejected", Value: strconv.FormatInt(input.Facts.EmailsRejected, 10),
				Note: emailRejectionNote(input.Facts.EmailRejectionReasons),
			})
		}
	}

	return metrics
}

// emailRejectionNote names the reasons candidates were refused, most common
// first, so a rejected count is an explanation rather than a mystery.
func emailRejectionNote(reasons map[string]int64) string {
	if len(reasons) == 0 {
		return "refused by the extraction rules"
	}
	keys := make([]string, 0, len(reasons))
	for key := range reasons {
		keys = append(keys, key)
	}
	sort.SliceStable(keys, func(a, b int) bool {
		if reasons[keys[a]] != reasons[keys[b]] {
			return reasons[keys[a]] > reasons[keys[b]]
		}
		return keys[a] < keys[b]
	})
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", strings.ReplaceAll(key, "_", " "), reasons[key]))
	}
	return strings.Join(parts, " · ")
}

// deduplicatingMetrics reports the run in the counting vocabulary defined by
// web/run_metrics.go, and in no other words.
//
// The stage used to report "Raw records / Duplicate matches / Merged records /
// Unresolved conflicts", where "raw records" was actually the business count,
// "duplicate matches" was repeated rows inside a file the merge had already
// de-repeated (structurally zero), and "merged records" counted entity merges
// — three different things whose numbers could never be reconciled against the
// coverage strip on the same page. Each metric below now names exactly what it
// counts, and says so when the evidence for it has not been published.
func deduplicatingMetrics(input jobPipelineInput) []jobPipelineMetric {
	observations := input.runObservations()

	observed := notReported
	repeated := notReported
	inFile := notReported
	if observations.Available {
		observed = strconv.FormatInt(observations.Observations, 10)
		repeated = fmt.Sprintf("%d (%.1f%% of observations)",
			observations.RepeatObservations, observations.RepeatSharePercent())
		inFile = strconv.FormatInt(observations.InFileDuplicateRows, 10)
	}

	merges := notReported
	if observations.HasEntityMerges {
		merges = strconv.FormatInt(observations.EntityMerges, 10)
	}

	unresolved := notReported
	if observations.HasUnresolvedDuplicates {
		unresolved = strconv.FormatInt(observations.UnresolvedDuplicates, 10)
	}

	return []jobPipelineMetric{{
		Group: pipelineGroupRunCount,
		Label: "Maps observations",
		Value: observed,
		Note:  "every time a search returned a business and the row was accepted",
	}, {
		Group: pipelineGroupRunCount,
		Label: "Repeated observations",
		Value: repeated,
		Note:  "a business an earlier search had already collected; its row was refreshed, not added",
	}, {
		Group: pipelineGroupRunCount,
		Label: "Rows repeated inside one search",
		Value: inFile,
		Note:  "duplicate rows dropped from a single search's own results",
	}, {
		Group: pipelineGroupRunCount,
		Label: "Unique businesses",
		Value: strconv.Itoa(input.Stats.UniqueBusinesses),
		Note:  "distinct businesses this run kept",
	}, {
		Group: pipelineGroupRunCount,
		Label: "Entity merges",
		Value: merges,
		Note:  "separate stored records folded into one; refreshing a row is not a merge",
	}, {
		Group: pipelineGroupRunCount,
		Label: "Unresolved duplicate candidates",
		Value: unresolved,
		Note:  "pairs flagged as probably the same business, still awaiting a decision",
	}}
}

// savingExportingMetrics reports rows committed, the output file, and the
// storage that file occupies.
func savingExportingMetrics(input jobPipelineInput) []jobPipelineMetric {
	output := "not created yet"
	storage := "0 B"
	if input.Stats.Rows > 0 || input.Stats.FileSizeBytes > 0 {
		output = input.Job.ID + ".csv"
		storage = humanBytes(input.Stats.FileSizeBytes)
	}

	return []jobPipelineMetric{
		{Label: "Rows committed", Value: strconv.Itoa(input.Stats.Rows)},
		{Label: "Output file", Value: output},
		{Label: "Storage used", Value: storage},
	}
}

func valueOrNotReported(value string) string {
	if strings.TrimSpace(value) == "" {
		return notReported
	}

	return value
}
