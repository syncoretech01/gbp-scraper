package web

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

// notReported is the single phrase the pipeline uses for evidence the worker
// has not published yet. Keeping it in one place is what lets app-monitor.js
// style every such value as an empty state instead of as data.
const notReported = "not reported yet"

// jobPipelineMetric is one named measurement inside a pipeline stage. Label is
// the specification's own wording so an operator can match the console to the
// documentation; Value is always a rendered string, because a stage that has
// not run reports a phrase rather than a misleading zero.
type jobPipelineMetric struct {
	Label string
	Value string
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

	return []jobPipelineMetric{
		{Label: "Cells created", Value: created},
		{Label: "Cells excluded", Value: excluded},
		{Label: "Geographic coverage", Value: locationSummary(input.Job.Data)},
		{Label: "Task estimate", Value: estimate},
	}
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
	}
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

	return []jobPipelineMetric{
		{Label: "Emails discovered", Value: strconv.FormatInt(emails, 10)},
		{Label: "Phones discovered", Value: strconv.FormatInt(phones, 10)},
		{Label: "Social links discovered", Value: socials},
	}
}

// deduplicatingMetrics reports raw records, matches, merges, and conflicts.
// A match is a raw row the deduper recognised as an existing business; a merge
// is a stored business folded into another; a conflict is a match the engine
// could not resolve, which is the difference between the two.
func deduplicatingMetrics(input jobPipelineInput) []jobPipelineMetric {
	matches := int64(input.Stats.Duplicates)
	merges := notReported
	conflicts := notReported
	if input.HasFacts {
		merges = strconv.FormatInt(input.Facts.Merged, 10)
		conflicts = strconv.FormatInt(max(0, matches-input.Facts.Merged), 10)
	}

	return []jobPipelineMetric{
		{Label: "Raw records", Value: strconv.FormatInt(input.RawRecords, 10)},
		{Label: "Duplicate matches", Value: strconv.FormatInt(matches, 10)},
		{Label: "Merged records", Value: merges},
		{Label: "Unresolved conflicts", Value: conflicts},
	}
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
