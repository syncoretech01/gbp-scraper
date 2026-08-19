package web

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

const maximumMapCellKeywordRunes = 500

// MapCellActivity is the durable task/result evidence associated with one
// source grid cell. Repositories return source-cell aggregates so geometry
// rendering remains isolated from storage details.
type MapCellActivity struct {
	SourceCell     string `json:"source_cell"`
	TaskCount      int64  `json:"task_count"`
	PendingTasks   int64  `json:"pending_tasks"`
	RunningTasks   int64  `json:"running_tasks"`
	CompletedTasks int64  `json:"completed_tasks"`
	FailedTasks    int64  `json:"failed_tasks"`
	BlockedTasks   int64  `json:"blocked_tasks"`
	SkippedTasks   int64  `json:"skipped_tasks"`
	WarningCount   int64  `json:"warning_count"`
	ResultCount    int64  `json:"result_count"`
	RawResultCount int64  `json:"raw_result_count"`
}

// DuplicateCount reports repeated observations beyond the unique businesses
// attributed to a cell.
func (activity MapCellActivity) DuplicateCount() int64 {
	return max(activity.RawResultCount-activity.ResultCount, 0)
}

// MapCoverageSummary provides inexpensive totals for the live coverage UI.
type MapCoverageSummary struct {
	WaitingCells        int64 `json:"waiting_cells"`
	RunningCells        int64 `json:"running_cells"`
	CompletedCells      int64 `json:"completed_cells"`
	PartialCells        int64 `json:"partial_cells"`
	FailedCells         int64 `json:"failed_cells"`
	BlockedCells        int64 `json:"blocked_cells"`
	PausedCells         int64 `json:"paused_cells"`
	EmptyCells          int64 `json:"empty_cells"`
	ResultCount         int64 `json:"result_count"`
	DuplicateCount      int64 `json:"duplicate_count"`
	UnmatchedTaskGroups int64 `json:"unmatched_task_groups"`
}

// MapCoveragePreview joins deterministic geometry with durable worker state.
type MapCoveragePreview struct {
	MapGridPreview
	JobID    string             `json:"job_id"`
	JobState string             `json:"job_state"`
	Summary  MapCoverageSummary `json:"summary"`
}

type mapCoverageRepository interface {
	MapCellActivity(context.Context, string) ([]MapCellActivity, error)
}

// PreviewMapCoverage overlays checkpointed task and source aggregates on the
// same deterministic cells used by the saved-area runner.
func (s *Service) PreviewMapCoverage(
	ctx context.Context,
	jobID string,
	geometry MapGeometry,
	cellSizeKM float64,
) (MapCoveragePreview, error) {
	jobID = strings.TrimSpace(jobID)
	if !validMapEntityID(jobID) {
		return MapCoveragePreview{}, fmt.Errorf("%w: job_id is required", ErrMapCellScrapeSelection)
	}
	job, err := s.Get(ctx, jobID)
	if err != nil {
		return MapCoveragePreview{}, err
	}
	repository, ok := s.repo.(mapCoverageRepository)
	if !ok {
		return MapCoveragePreview{}, ErrMapCoverageUnsupported
	}
	preview, err := PreviewMapGrid(geometry, cellSizeKM, maximumMapGridCells)
	if err != nil {
		return MapCoveragePreview{}, err
	}
	activities, err := repository.MapCellActivity(ctx, jobID)
	if err != nil {
		return MapCoveragePreview{}, err
	}
	state, stateErr := stateFromLegacyStatus(job.Status)
	if runtime, runtimeErr := s.GetRuntime(ctx, jobID); runtimeErr == nil {
		state = runtime.State
	} else if stateErr != nil {
		return MapCoveragePreview{}, runtimeErr
	}

	bySource := make(map[string]MapCellActivity, len(activities))
	for _, activity := range activities {
		activity.SourceCell = strings.TrimSpace(activity.SourceCell)
		if activity.SourceCell == "" {
			continue
		}
		bySource[activity.SourceCell] = activity
	}
	matched := make(map[string]struct{}, len(activities))
	coverage := MapCoveragePreview{
		MapGridPreview: preview,
		JobID:          jobID,
		JobState:       string(state),
	}
	for index := range coverage.Cells {
		cell := &coverage.Cells[index]
		activity, sourceKey, found := mapActivityForCell(bySource, *cell)
		if found {
			matched[sourceKey] = struct{}{}
			applyMapCellActivity(cell, activity, state)
		}
		addMapCoverageSummary(&coverage.Summary, *cell)
	}
	coverage.Summary.UnmatchedTaskGroups = int64(len(bySource) - len(matched))

	return coverage, nil
}

func mapActivityForCell(activities map[string]MapCellActivity, cell MapGridCell) (MapCellActivity, string, bool) {
	if activity, ok := activities[cell.ID]; ok {
		return activity, cell.ID, true
	}
	coordinateKey := fmt.Sprintf("%.6f,%.6f", cell.Centre.Latitude, cell.Centre.Longitude)
	activity, ok := activities[coordinateKey]

	return activity, coordinateKey, ok
}

func applyMapCellActivity(cell *MapGridCell, activity MapCellActivity, jobState jobruntime.State) {
	cell.TaskCount = activity.TaskCount
	cell.PendingTasks = activity.PendingTasks
	cell.RunningTasks = activity.RunningTasks
	cell.CompletedTasks = activity.CompletedTasks
	cell.FailedTasks = activity.FailedTasks
	cell.BlockedTasks = activity.BlockedTasks
	cell.SkippedTasks = activity.SkippedTasks
	cell.WarningCount = activity.WarningCount
	cell.ResultCount = activity.ResultCount
	cell.DuplicateCount = activity.DuplicateCount()
	cell.Empty = activity.TaskCount > 0 && activity.PendingTasks == 0 && activity.RunningTasks == 0 &&
		activity.ResultCount == 0
	cell.State = mapCellCoverageState(activity, jobState)
}

func mapCellCoverageState(activity MapCellActivity, jobState jobruntime.State) string {
	if activity.RunningTasks > 0 {
		return "running"
	}
	unfinished := activity.PendingTasks > 0
	if jobState == jobruntime.StatePaused && unfinished {
		return "paused"
	}
	if activity.BlockedTasks > 0 && activity.CompletedTasks == 0 {
		return "blocked"
	}
	if activity.FailedTasks > 0 && activity.CompletedTasks == 0 {
		return "failed"
	}
	if activity.FailedTasks > 0 || activity.BlockedTasks > 0 || activity.SkippedTasks > 0 || activity.WarningCount > 0 {
		return "partial"
	}
	if activity.CompletedTasks > 0 && unfinished {
		return "partial"
	}
	if activity.TaskCount > 0 && activity.CompletedTasks >= activity.TaskCount {
		return "completed"
	}
	if activity.TaskCount == 0 && activity.ResultCount > 0 {
		return "completed"
	}

	return "waiting"
}

func addMapCoverageSummary(summary *MapCoverageSummary, cell MapGridCell) {
	switch cell.State {
	case "running":
		summary.RunningCells++
	case "completed":
		summary.CompletedCells++
	case "partial":
		summary.PartialCells++
	case "failed":
		summary.FailedCells++
	case "blocked":
		summary.BlockedCells++
	case "paused":
		summary.PausedCells++
	default:
		summary.WaitingCells++
	}
	if cell.Empty {
		summary.EmptyCells++
	}
	summary.ResultCount += cell.ResultCount
	summary.DuplicateCount += cell.DuplicateCount
}

type mapCoverageInput struct {
	AreaID    string          `json:"area_id,omitempty"`
	GeoJSON   json.RawMessage `json:"geojson,omitempty"`
	CellSizeK float64         `json:"cell_size_km"`
	JobID     string          `json:"job_id"`
}

func (s *Server) apiMapCoverage(w http.ResponseWriter, r *http.Request) {
	raw, err := readBoundedMapBody(w, r)
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	var input mapCoverageInput
	if err := decodeStrictMapJSON(raw, &input); err != nil {
		renderMapAPIError(w, err)
		return
	}
	geometry, err := s.resolveMapGeometry(r.Context(), input.AreaID, input.GeoJSON)
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	coverage, err := s.svc.PreviewMapCoverage(r.Context(), input.JobID, geometry, input.CellSizeK)
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	renderJSON(w, http.StatusOK, localAPIEnvelope{Data: coverage})
}

type mapResultExportInput struct {
	AreaID  string          `json:"area_id,omitempty"`
	GeoJSON json.RawMessage `json:"geojson,omitempty"`
	Search  ResultSearch    `json:"search"`
}

func (s *Server) apiExportMapResults(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	raw, err := readBoundedMapBody(w, r)
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	var input mapResultExportInput
	if err := decodeStrictMapJSON(raw, &input); err != nil {
		renderMapAPIError(w, err)
		return
	}
	geometry, err := s.resolveMapGeometry(r.Context(), input.AreaID, input.GeoJSON)
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	results, err := s.mapResultsForExport(r.Context(), input.Search, geometry)
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "geojson" {
		s.renderMapResultsGeoJSON(w, results)
		return
	}
	if format != "" && format != "csv" {
		renderLocalAPIError(w, http.StatusUnprocessableEntity, "invalid_export_format", "Map exports support csv or geojson")
		return
	}
	if err := renderMapResultsCSV(w, results); err != nil {
		renderMapAPIError(w, err)
	}
}

func (s *Server) mapResultsForExport(
	ctx context.Context,
	search ResultSearch,
	geometry MapGeometry,
) ([]BusinessResult, error) {
	results := make([]BusinessResult, 0)
	search.Limit = 250
	search.Offset = 0
	_, err := s.svc.SearchAllBusinessesInArea(ctx, search, geometry, maximumMapExportRows, func(result BusinessResult) error {
		results = append(results, result)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return results, nil
}

var mapCSVHeaders = []string{
	"id", "name", "category", "address", "city", "state", "postal_code", "country",
	"latitude", "longitude", "phone", "email", "website", "rating", "review_count",
	"maps_url", "source_job_id", "source_query", "source_cell", "scraped_at",
}

func renderMapResultsCSV(w http.ResponseWriter, results []BusinessResult) error {
	buffer := bytes.NewBuffer(nil)
	writer := csv.NewWriter(buffer)
	if err := writer.Write(mapCSVHeaders); err != nil {
		return fmt.Errorf("write map CSV header: %w", err)
	}
	for _, result := range results {
		row := []string{
			result.ID, result.Name, result.PrimaryCategory, result.Address, result.City, result.State,
			result.PostalCode, result.Country, mapFloatPointer(result.Latitude), mapFloatPointer(result.Longitude),
			result.Phone, result.PrimaryEmail, result.Website, mapFloatPointer(result.Rating), mapIntPointer(result.ReviewCount),
			result.MapsURL, result.SourceJobID, result.SourceQuery, result.SourceCell, result.ScrapedAt.Format(time.RFC3339),
		}
		for index := range row {
			row[index] = safeMapSpreadsheetValue(row[index])
		}
		if err := writer.Write(row); err != nil {
			return fmt.Errorf("write map CSV row: %w", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("finish map CSV export: %w", err)
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "map-businesses-"+time.Now().UTC().Format("20060102-150405")+".csv"))
	w.WriteHeader(http.StatusOK)
	_, err := w.Write(buffer.Bytes())

	return err
}

func (s *Server) renderMapResultsGeoJSON(w http.ResponseWriter, results []BusinessResult) {
	features := make([]map[string]any, 0, len(results))
	for _, result := range results {
		if result.Latitude == nil || result.Longitude == nil {
			continue
		}
		properties := map[string]any{
			"id": result.ID, "name": result.Name, "category": result.PrimaryCategory,
			"address": result.Address, "city": result.City, "state": result.State,
			"postal_code": result.PostalCode, "country": result.Country,
			"phone": result.Phone, "email": result.PrimaryEmail, "website": result.Website,
			"rating": result.Rating, "review_count": result.ReviewCount, "maps_url": result.MapsURL,
			"source_job_id": result.SourceJobID, "source_query": result.SourceQuery,
			"source_cell": result.SourceCell, "scraped_at": result.ScrapedAt,
		}
		features = append(features, map[string]any{
			"type": "Feature",
			"geometry": map[string]any{
				"type": "Point", "coordinates": []float64{*result.Longitude, *result.Latitude},
			},
			"properties": properties,
		})
	}
	payload := map[string]any{"type": "FeatureCollection", "features": features}
	w.Header().Set("Content-Type", "application/geo+json")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "map-businesses-"+time.Now().UTC().Format("20060102-150405")+".geojson"))
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(payload); err != nil {
		return
	}
}

func mapFloatPointer(value *float64) string {
	if value == nil {
		return ""
	}

	return strconv.FormatFloat(*value, 'f', -1, 64)
}

func mapIntPointer(value *int64) string {
	if value == nil {
		return ""
	}

	return strconv.FormatInt(*value, 10)
}

func safeMapSpreadsheetValue(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + value
	}

	return value
}

type mapCellScrapeInput struct {
	AreaID    string          `json:"area_id,omitempty"`
	GeoJSON   json.RawMessage `json:"geojson,omitempty"`
	CellSizeK float64         `json:"cell_size_km"`
	JobID     string          `json:"job_id"`
	CellIDs   []string        `json:"cell_ids"`
	Action    string          `json:"action"`
	Keyword   string          `json:"keyword,omitempty"`
	Template  string          `json:"template_id,omitempty"`
}

func (s *Server) apiRescrapeMapCells(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	raw, err := readBoundedMapBody(w, r)
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	var input mapCellScrapeInput
	if err := decodeStrictMapJSON(raw, &input); err != nil {
		renderMapAPIError(w, err)
		return
	}
	geometry, err := s.resolveMapGeometry(r.Context(), input.AreaID, input.GeoJSON)
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	job, err := s.createMapCellScrape(r.Context(), input, geometry)
	if err != nil {
		renderMapAPIError(w, err)
		return
	}
	renderJSON(w, http.StatusCreated, localAPIEnvelope{Data: map[string]any{
		"id": job.ID, "name": job.Name, "state": jobruntime.StateQueued,
		"selected_cells": len(input.CellIDs), "url": "/app/jobs/" + job.ID,
	}})
}

func (s *Server) createMapCellScrape(
	ctx context.Context,
	input mapCellScrapeInput,
	geometry MapGeometry,
) (Job, error) {
	input.JobID = strings.TrimSpace(input.JobID)
	if !validMapEntityID(input.JobID) {
		return Job{}, fmt.Errorf("%w: choose a source job", ErrMapCellScrapeSelection)
	}
	preview, err := PreviewMapGrid(geometry, input.CellSizeK, maximumMapGridCells)
	if err != nil {
		return Job{}, err
	}
	selected, err := validateSelectedMapCells(preview, geometry, input.CellIDs)
	if err != nil {
		return Job{}, err
	}
	source, err := s.svc.Get(ctx, input.JobID)
	if err != nil {
		return Job{}, err
	}

	action := strings.ToLower(strings.TrimSpace(input.Action))
	keywords := append([]string(nil), source.Data.Keywords...)
	nameSuffix := "selected cells"
	switch action {
	case "retry":
		coverage, coverageErr := s.svc.PreviewMapCoverage(ctx, input.JobID, geometry, input.CellSizeK)
		if coverageErr != nil {
			return Job{}, coverageErr
		}
		byID := make(map[string]MapGridCell, len(coverage.Cells))
		for _, cell := range coverage.Cells {
			byID[cell.ID] = cell
		}
		for id := range selected {
			cell := byID[id]
			if cell.FailedTasks == 0 && cell.BlockedTasks == 0 && !cell.Empty {
				return Job{}, fmt.Errorf("%w: cell %s is not failed, blocked, or empty", ErrMapCellScrapeSelection, id)
			}
		}
		nameSuffix = "failed or empty cells"
	case "keyword":
		keyword := strings.TrimSpace(input.Keyword)
		if keyword == "" || utf8.RuneCountInString(keyword) > maximumMapCellKeywordRunes || strings.ContainsAny(keyword, "\r\n") {
			return Job{}, fmt.Errorf("%w: keyword must contain 1 to %d characters on one line", ErrMapCellScrapeSelection, maximumMapCellKeywordRunes)
		}
		keywords = []string{keyword}
		nameSuffix = keyword
	case "template":
		templateID := strings.TrimSpace(input.Template)
		if !validMapEntityID(templateID) {
			return Job{}, fmt.Errorf("%w: choose a saved keyword group", ErrMapCellScrapeSelection)
		}
		template, templateErr := s.svc.GetScrapeTemplate(ctx, templateID)
		if templateErr != nil {
			return Job{}, templateErr
		}
		keywords = append([]string(nil), template.Configuration.Keywords...)
		if !validMapCellKeywords(keywords) {
			return Job{}, fmt.Errorf("%w: saved keyword group is empty or invalid", ErrMapCellScrapeSelection)
		}
		nameSuffix = firstNonEmpty(strings.TrimSpace(template.Name), "saved keyword group")
	default:
		return Job{}, fmt.Errorf("%w: action must be retry, keyword, or template", ErrMapCellScrapeSelection)
	}
	selectedGeometry, err := mapGeometryForSelectedCells(geometry, preview, selected)
	if err != nil {
		return Job{}, err
	}

	created := source
	created.ID = uuid.NewString()
	created.Name = boundedMapJobName(strings.TrimSpace(source.Name) + " - " + nameSuffix)
	created.Date = time.Now().UTC()
	created.Status = StatusPending
	created.Data.Keywords = keywords
	created.Data.Proxies = nil
	created.Data.SavedAreaID = ""
	created.Data.AreaGeoJSON = string(selectedGeometry.GeoJSON())
	created.Data.GridBBox = ""
	created.Data.GridCellKM = input.CellSizeK
	created.Data.FastMode = false
	centre := selectedGeometry.Centre()
	created.Data.Lat = strconv.FormatFloat(centre.Latitude, 'f', -1, 64)
	created.Data.Lon = strconv.FormatFloat(centre.Longitude, 'f', -1, 64)
	if radius, ok := selectedGeometry.CircleRadiusMetres(); ok {
		created.Data.Radius = int(radius)
	}
	if err := created.Validate(); err != nil {
		return Job{}, fmt.Errorf("%w: cloned job configuration is invalid: %v", ErrMapCellScrapeSelection, err)
	}
	if err := s.svc.CreateWithState(ctx, &created, jobruntime.StateQueued); err != nil {
		return Job{}, err
	}

	return created, nil
}

func validMapCellKeywords(keywords []string) bool {
	if len(keywords) == 0 || len(keywords) > 1_000 {
		return false
	}
	for _, keyword := range keywords {
		keyword = strings.TrimSpace(keyword)
		if keyword == "" || utf8.RuneCountInString(keyword) > maximumMapCellKeywordRunes || strings.ContainsAny(keyword, "\r\n") {
			return false
		}
	}

	return true
}

func validateSelectedMapCells(
	preview MapGridPreview,
	geometry MapGeometry,
	requested []string,
) (map[string]struct{}, error) {
	if len(requested) == 0 || len(requested) > maximumMapGridCells {
		return nil, fmt.Errorf("%w: choose between 1 and %d cells", ErrMapCellScrapeSelection, maximumMapGridCells)
	}
	known := make(map[string]struct{}, len(preview.Cells))
	for _, cell := range preview.Cells {
		known[cell.ID] = struct{}{}
	}
	excluded := make(map[string]struct{}, len(geometry.ExcludedCellIDs()))
	for _, id := range geometry.ExcludedCellIDs() {
		excluded[id] = struct{}{}
	}
	selected := make(map[string]struct{}, len(requested))
	for _, rawID := range requested {
		id := strings.TrimSpace(rawID)
		if !validMapEntityID(id) {
			return nil, fmt.Errorf("%w: invalid cell ID", ErrMapCellScrapeSelection)
		}
		if _, ok := known[id]; !ok {
			return nil, fmt.Errorf("%w: cell %s is not in the current grid", ErrMapCellScrapeSelection, id)
		}
		if _, removed := excluded[id]; removed {
			return nil, fmt.Errorf("%w: cell %s is excluded from the current area", ErrMapCellScrapeSelection, id)
		}
		selected[id] = struct{}{}
	}

	return selected, nil
}

func mapGeometryForSelectedCells(
	geometry MapGeometry,
	preview MapGridPreview,
	selected map[string]struct{},
) (MapGeometry, error) {
	var feature map[string]any
	if err := json.Unmarshal(geometry.GeoJSON(), &feature); err != nil {
		return MapGeometry{}, fmt.Errorf("%w: could not copy selected geometry", ErrMapCellScrapeSelection)
	}
	properties, ok := feature["properties"].(map[string]any)
	if !ok || properties == nil {
		properties = make(map[string]any)
	}
	excluded := make([]string, 0, len(preview.Cells)-len(selected))
	for _, cell := range preview.Cells {
		if _, keep := selected[cell.ID]; !keep {
			excluded = append(excluded, cell.ID)
		}
	}
	properties["excluded_cells"] = excluded
	feature["properties"] = properties
	raw, err := json.Marshal(feature)
	if err != nil {
		return MapGeometry{}, fmt.Errorf("%w: could not encode selected geometry", ErrMapCellScrapeSelection)
	}

	return ParseMapGeometry(raw)
}

func boundedMapJobName(value string) string {
	const maximumRunes = 120
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= maximumRunes {
		return value
	}
	runes := []rune(value)

	return strings.TrimSpace(string(runes[:maximumRunes-1])) + "…"
}
