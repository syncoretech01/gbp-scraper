package web

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/web/jobruntime"
)

const maxWizardUploadBytes = 2 << 20

func (s *Server) createScrapeFromWizard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxWizardUploadBytes*2)
	var parseErr error
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		parseErr = r.ParseMultipartForm(maxWizardUploadBytes)
	} else {
		parseErr = r.ParseForm()
	}

	if parseErr != nil {
		http.Error(w, "invalid or oversized form", http.StatusUnprocessableEntity)

		return
	}

	if !s.requireCSRF(w, r) {
		return
	}

	job, state, err := parseWizardJob(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)

		return
	}

	var savedTemplateID string
	if r.FormValue("save_template") == "on" {
		name := strings.TrimSpace(r.FormValue("template_name"))
		if name == "" {
			name = job.Name
		}
		if len(name) > 120 {
			http.Error(w, "template name must be at most 120 characters", http.StatusUnprocessableEntity)
			return
		}
		now := time.Now().UTC()
		// The stored configuration must not carry the link to whichever
		// template this job was started from, or the new template would
		// report another template's run history.
		configuration := job.Data
		configuration.TemplateID = ""
		template := ScrapeTemplate{
			ID: uuid.NewString(), Name: name, Description: "Saved from the New Scrape wizard",
			Configuration: configuration, CreatedAt: now, UpdatedAt: now,
		}
		if err := s.svc.SaveScrapeTemplate(r.Context(), template); err != nil {
			http.Error(w, "could not save scrape template", http.StatusInternalServerError)
			return
		}
		savedTemplateID = template.ID
	}

	if err := s.svc.CreateWithState(r.Context(), &job, state); err != nil {
		if savedTemplateID != "" {
			_ = s.svc.DeleteScrapeTemplate(r.Context(), savedTemplateID)
		}
		if err == ErrLifecycleUnsupported {
			http.Error(w, "saving drafts requires the upgraded local database", http.StatusNotImplemented)

			return
		}

		http.Error(w, "could not save job", http.StatusInternalServerError)

		return
	}

	if templateID := strings.TrimSpace(r.FormValue("template_id")); templateID != "" {
		_ = s.svc.RecordScrapeTemplateUse(r.Context(), templateID, time.Now().UTC())
	}

	// The data-field selection and the post-collection filters are only real
	// if something uses them. Both are materialised here: the filters as a
	// saved result view and the field selection as a repeatable export
	// profile. Neither is required for the job to run, so a repository that
	// cannot store them is not an error.
	if _, _, viewErr := s.svc.SaveJobCollectionView(r.Context(), job); viewErr != nil {
		s.recordJobPlanWarning(r.Context(), job.ID, "saved view", viewErr)
	}

	if _, _, presetErr := s.svc.SaveJobFieldExportPreset(r.Context(), job); presetErr != nil {
		s.recordJobPlanWarning(r.Context(), job.ID, "export profile", presetErr)
	}

	http.Redirect(w, r, "/app/jobs/"+job.ID, http.StatusSeeOther)
}

func parseWizardJob(r *http.Request) (Job, jobruntime.State, error) {
	action := strings.TrimSpace(r.FormValue("_action"))
	state := jobruntime.StateQueued
	if action == "draft" {
		state = jobruntime.StateDraft
	} else if action != "" && action != "start" {
		return Job{}, "", fmt.Errorf("invalid job action")
	}

	if state == jobruntime.StateQueued && r.FormValue("responsible_use") != "accepted" {
		return Job{}, "", fmt.Errorf("responsible-use acknowledgement is required")
	}

	// A parameterised configuration generates its own query lines, so the
	// typed list may legitimately be empty in that case and only in that case.
	parameters := (&JobParameters{
		Categories: splitFilterList(r.FormValue("parameter_categories")),
		Locations:  splitFilterList(r.FormValue("parameter_locations")),
		Pattern:    strings.TrimSpace(r.FormValue("parameter_pattern")),
		Replace:    r.FormValue("parameter_replace") == "on",
	}).Normalized()
	if err := parameters.Validate(); err != nil {
		return Job{}, "", err
	}

	keywords, err := wizardKeywords(r, parameters != nil)
	if err != nil {
		return Job{}, "", err
	}

	zoom, err := requiredFormInt(r, "zoom")
	if err != nil {
		return Job{}, "", err
	}

	radius, err := requiredFormInt(r, "radius")
	if err != nil {
		return Job{}, "", err
	}

	depth, err := requiredFormInt(r, "depth")
	if err != nil {
		return Job{}, "", err
	}

	concurrency, err := optionalFormInt(r, "concurrency")
	if err != nil {
		return Job{}, "", err
	}

	browserPool, err := optionalFormInt(r, "browser_pool_size")
	if err != nil {
		return Job{}, "", err
	}

	taskWorkers, err := optionalFormInt(r, "task_workers")
	if err != nil {
		return Job{}, "", err
	}

	pagesBrowser, err := optionalFormInt(r, "pages_per_browser")
	if err != nil {
		return Job{}, "", err
	}
	maxRecords, err := optionalFormInt(r, "max_records")
	if err != nil {
		return Job{}, "", err
	}
	retryCount, err := optionalFormInt(r, "retry_count")
	if err != nil {
		return Job{}, "", err
	}
	retryDelay, err := optionalFormDuration(r, "retry_delay")
	if err != nil {
		return Job{}, "", err
	}
	pageTimeout, err := optionalFormDuration(r, "page_timeout")
	if err != nil {
		return Job{}, "", err
	}
	randomDelayMin, err := optionalFormDuration(r, "random_delay_min")
	if err != nil {
		return Job{}, "", err
	}
	randomDelayMax, err := optionalFormDuration(r, "random_delay_max")
	if err != nil {
		return Job{}, "", err
	}
	checkpointSeconds, err := optionalFormInt(r, "checkpoint_seconds")
	if err != nil {
		return Job{}, "", err
	}

	lowDiskMB, err := optionalFormInt(r, "low_disk_mb")
	if err != nil {
		return Job{}, "", err
	}
	memoryCeilingMB, err := optionalFormInt(r, "memory_ceiling_mb")
	if err != nil {
		return Job{}, "", err
	}
	enrichmentMaxPages, err := optionalFormInt(r, "enrichment_max_pages")
	if err != nil {
		return Job{}, "", err
	}
	enrichmentTimeout, err := optionalFormInt(r, "enrichment_timeout_seconds")
	if err != nil {
		return Job{}, "", err
	}
	enrichmentInternalLinksValue := strings.TrimSpace(r.FormValue("enrichment_internal_links"))
	enrichmentInternalLinks, err := optionalFormInt(r, "enrichment_internal_links")
	if err != nil {
		return Job{}, "", err
	}
	enrichmentStaleHours, err := optionalFormInt(r, "enrichment_stale_hours")
	if err != nil {
		return Job{}, "", err
	}
	if enrichmentStaleHours < 0 || enrichmentStaleHours > MaximumEnrichmentStaleHours {
		return Job{}, "", fmt.Errorf("re-audit window must be between 0 and %d hours", MaximumEnrichmentStaleHours)
	}

	maxTime, err := time.ParseDuration(strings.TrimSpace(r.FormValue("maxtime")))
	if err != nil || maxTime < 3*time.Minute {
		return Job{}, "", fmt.Errorf("maximum runtime must be a duration of at least 3m")
	}

	latitude := strings.TrimSpace(r.FormValue("latitude"))
	longitude := strings.TrimSpace(r.FormValue("longitude"))
	fastMode := r.FormValue("fastmode") == "on"

	websiteEnrichment := r.FormValue("email") == "on"
	data := JobData{
		Keywords:      keywords,
		Lang:          strings.ToLower(strings.TrimSpace(r.FormValue("lang"))),
		Zoom:          zoom,
		Lat:           latitude,
		Lon:           longitude,
		LocationLabel: strings.TrimSpace(r.FormValue("location_label")),
		FastMode:      fastMode,
		Radius:        radius,
		Depth:         depth,
		Email:         websiteEnrichment,
		ExtraReviews:  r.FormValue("extra_reviews") == "on",
		MaxTime:       maxTime,
		Concurrency:   concurrency,
		TaskWorkers:   taskWorkers,
		BrowserPool:   browserPool,
		PagesBrowser:  pagesBrowser,
		MaxRecords:    maxRecords,
		RetryCount:    retryCount,
		RetryDelay:    retryDelay,
		RetryConfigured: strings.TrimSpace(r.FormValue("retry_count")) != "" ||
			strings.TrimSpace(r.FormValue("retry_delay")) != "",
		PageTimeout:       pageTimeout,
		RandomDelayMin:    randomDelayMin,
		RandomDelayMax:    randomDelayMax,
		Headfull:          r.FormValue("headfull") == "on",
		LoadImages:        r.FormValue("load_images") == "on",
		Adaptive:          r.FormValue("adaptive_performance") == "on",
		CheckpointSeconds: max(0, checkpointSeconds),
		LowDiskBytes:      uint64(max(0, lowDiskMB)) * 1024 * 1024,
		// An empty field is zero, which means "no ceiling" and keeps exactly
		// the behaviour a job had before the control existed.
		MemoryCeilingBytes: uint64(max(0, memoryCeilingMB)) * 1024 * 1024,
		ProxyPoolID:        strings.TrimSpace(r.FormValue("proxy_pool_id")),
		SavedAreaID:        strings.TrimSpace(r.FormValue("saved_area_id")),
		IncrementalMode:    strings.TrimSpace(r.FormValue("incremental_mode")),
	}

	fields, err := wizardFieldSelection(r)
	if err != nil {
		return Job{}, "", err
	}

	data.Fields = fields

	filters, err := wizardResultFilters(r)
	if err != nil {
		return Job{}, "", err
	}

	data.ResultFilters = filters
	data.TemplateID = strings.TrimSpace(r.FormValue("template_id"))
	data.Parameters = parameters

	// Parameterised configurations regenerate their query lines here, so a
	// template saved with them produces a fresh plan on every future run.
	data, err = ApplyJobParameters(data)
	if err != nil {
		return Job{}, "", err
	}
	if websiteEnrichment {
		if enrichmentMaxPages == 0 {
			enrichmentMaxPages = 3
		}
		if enrichmentTimeout == 0 {
			enrichmentTimeout = 10
		}
		if enrichmentInternalLinksValue == "" {
			enrichmentInternalLinks = 10
		}
		data.Enrichment = &JobEnrichmentOptions{
			Website:               true,
			Emails:                true,
			SocialProfiles:        true,
			Scope:                 strings.TrimSpace(r.FormValue("enrichment_scope")),
			MaxPages:              enrichmentMaxPages,
			TimeoutSeconds:        enrichmentTimeout,
			MaxInternalLinkChecks: enrichmentInternalLinks,
			DisableInternalChecks: enrichmentInternalLinks == 0,
			CheckMX:               r.FormValue("enrichment_check_mx") == "on",
			CaptureScreenshot:     r.FormValue("enrichment_capture_screenshot") == "on",
			StaleAfterHours:       enrichmentStaleHours,
			ForceReaudit:          r.FormValue("enrichment_force_reaudit") == "on",
			AdaptiveTimeout:       r.FormValue("enrichment_adaptive_timeout") == "on",
			IncludeURLPatterns:    splitFilterList(r.FormValue("enrichment_include_url_patterns")),
			ExcludeURLPatterns:    splitFilterList(r.FormValue("enrichment_exclude_url_patterns")),
		}
		// The stale-contacts rescan mode is the one incremental mode that
		// changes work actually done, because the website audit is local. It
		// never forces a re-audit: that would be the opposite of the mode.
		if data.IncrementalMode == IncrementalModeStaleContacts {
			data.Enrichment.ForceReaudit = false
			if data.Enrichment.StaleAfterHours <= 0 {
				data.Enrichment.StaleAfterHours = DefaultEnrichmentStaleHours
			}
		}
	}
	if data.Lang == "" {
		data.Lang = "en"
	}

	coverage, err := wizardCoverageOptions(r)
	if err != nil {
		return Job{}, "", err
	}

	data.Coverage = coverage

	areaSnapshot := strings.TrimSpace(r.FormValue("area_geojson"))
	if data.SavedAreaID != "" && areaSnapshot == "" {
		return Job{}, "", fmt.Errorf("saved area snapshot is required")
	}
	if areaSnapshot != "" {
		geometry, geometryErr := ParseMapGeometry([]byte(areaSnapshot))
		if geometryErr != nil {
			return Job{}, "", fmt.Errorf("invalid saved area: %w", geometryErr)
		}
		if fastMode && geometry.Kind() != "circle" {
			return Job{}, "", fmt.Errorf("fast mode supports saved circles only; use grid mode for polygons")
		}
		centre := geometry.Centre()
		data.Lat = strconv.FormatFloat(centre.Latitude, 'f', 8, 64)
		data.Lon = strconv.FormatFloat(centre.Longitude, 'f', 8, 64)
		data.AreaGeoJSON = string(geometry.GeoJSON())
		if radiusMetres, ok := geometry.CircleRadiusMetres(); ok {
			data.Radius = max(1, int(math.Ceil(radiusMetres)))
		}
		if !fastMode {
			bounds := geometry.Bounds()
			data.GridBBox = fmt.Sprintf("%.8f,%.8f,%.8f,%.8f",
				bounds.MinLatitude, bounds.MinLongitude, bounds.MaxLatitude, bounds.MaxLongitude)
			cellKM, parseErr := strconv.ParseFloat(strings.TrimSpace(r.FormValue("grid_cell_km")), 64)
			if parseErr != nil || cellKM <= 0 {
				return Job{}, "", fmt.Errorf("grid cell size must be greater than 0")
			}
			data.GridCellKM = cellKM
		}
	} else if !fastMode {
		lat, latErr := strconv.ParseFloat(latitude, 64)
		lon, lonErr := strconv.ParseFloat(longitude, 64)
		if latErr != nil || lonErr != nil || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
			return Job{}, "", fmt.Errorf("valid latitude and longitude are required")
		}

		cellKM, parseErr := strconv.ParseFloat(strings.TrimSpace(r.FormValue("grid_cell_km")), 64)
		if parseErr != nil || cellKM <= 0 {
			return Job{}, "", fmt.Errorf("grid cell size must be greater than 0")
		}

		data.GridBBox = radiusBoundingBox(lat, lon, float64(radius))
		data.GridCellKM = cellKM
	}

	job := Job{
		ID:     uuid.NewString(),
		Name:   strings.TrimSpace(r.FormValue("name")),
		Date:   time.Now().UTC(),
		Status: StatusPending,
		Data:   data,
	}

	if err := job.Validate(); err != nil {
		return Job{}, "", err
	}

	return job, state, nil
}

// wizardKeywords collects the typed and uploaded query lines. allowEmpty is
// true only when the configuration carries parameters that generate their own
// lines, so an ordinary job still cannot be created without a query.
func wizardKeywords(r *http.Request, allowEmpty bool) ([]string, error) {
	values := splitNonEmptyLines(r.FormValue("keywords"))

	if r.MultipartForm != nil {
		file, header, err := r.FormFile("keywords_file")
		if err == nil {
			defer func() { _ = file.Close() }()

			uploaded, readErr := readKeywordUpload(file, header)
			if readErr != nil {
				return nil, readErr
			}

			values = append(values, uploaded...)
		} else if err != http.ErrMissingFile {
			return nil, fmt.Errorf("read keyword upload: %w", err)
		}
	}

	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value))
		if key == "" {
			continue
		}

		if _, exists := seen[key]; exists {
			continue
		}

		seen[key] = struct{}{}
		unique = append(unique, strings.TrimSpace(value))
	}

	if len(unique) == 0 && !allowEmpty {
		return nil, fmt.Errorf("at least one query is required")
	}

	return unique, nil
}

func readKeywordUpload(file multipart.File, header *multipart.FileHeader) ([]string, error) {
	limited := io.LimitReader(file, maxWizardUploadBytes+1)
	content, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read keyword upload: %w", err)
	}

	if len(content) > maxWizardUploadBytes {
		return nil, fmt.Errorf("keyword upload exceeds 2 MB")
	}

	if strings.EqualFold(filepath.Ext(header.Filename), ".csv") {
		reader := csv.NewReader(strings.NewReader(string(content)))
		reader.FieldsPerRecord = -1
		var values []string

		for rowIndex := 0; ; rowIndex++ {
			row, readErr := reader.Read()
			if readErr == io.EOF {
				break
			}

			if readErr != nil {
				return nil, fmt.Errorf("parse keyword CSV: %w", readErr)
			}

			if len(row) == 0 {
				continue
			}

			value := strings.TrimSpace(row[0])
			if rowIndex == 0 && (strings.EqualFold(value, "keyword") || strings.EqualFold(value, "query")) {
				continue
			}

			values = append(values, value)
		}

		return values, nil
	}

	return splitNonEmptyLines(string(content)), nil
}

func splitNonEmptyLines(value string) []string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")

	lines := strings.Split(value, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			result = append(result, trimmed)
		}
	}

	return result
}

// wizardCoverageOptions maps the optional adaptive-discovery form fields to
// JobData.Coverage. When none of the fields is present the result is nil,
// which keeps exactly the historical scrape behaviour.
func wizardCoverageOptions(r *http.Request) (*CoverageOptions, error) {
	autoStop := r.FormValue("coverage_auto_stop") == "on"
	windowValue := strings.TrimSpace(r.FormValue("coverage_saturation_window"))
	ratioValue := strings.TrimSpace(r.FormValue("coverage_min_new_ratio"))
	expansionsValue := strings.TrimSpace(r.FormValue("coverage_max_expansions"))
	minNewValue := strings.TrimSpace(r.FormValue("coverage_expansion_min_new"))
	// An explicit zero-yield choice is a tri-state: present and on, present
	// and off, or absent (follow AutoStop). The hidden companion field is
	// what distinguishes "the operator unticked it" from "the form has no
	// such control at all".
	emptyWindowChoice := strings.TrimSpace(r.FormValue("coverage_stop_on_empty_window_set"))
	emptyWindowValue := r.FormValue("coverage_stop_on_empty_window") == "on"

	if !autoStop && windowValue == "" && ratioValue == "" &&
		expansionsValue == "" && minNewValue == "" && emptyWindowChoice == "" {
		return nil, nil
	}

	options := &CoverageOptions{AutoStop: autoStop}

	if emptyWindowChoice != "" {
		options.StopOnEmptyWindow = &emptyWindowValue
	}

	if windowValue != "" {
		window, err := strconv.Atoi(windowValue)
		if err != nil || window < minCoverageSaturationWindow || window > maxCoverageSaturationWindow {
			return nil, fmt.Errorf("coverage saturation window must be a whole number between %d and %d",
				minCoverageSaturationWindow, maxCoverageSaturationWindow)
		}

		options.SaturationWindow = window
	}

	if ratioValue != "" {
		ratio, err := strconv.ParseFloat(ratioValue, 64)
		if err != nil || ratio < minCoverageMinNewRatio || ratio > maxCoverageMinNewRatio {
			return nil, fmt.Errorf("coverage minimum new ratio must be a number between %.2f and %.2f",
				minCoverageMinNewRatio, maxCoverageMinNewRatio)
		}

		options.MinNewRatio = ratio
	}

	if expansionsValue != "" {
		expansions, err := strconv.Atoi(expansionsValue)
		if err != nil || expansions < 0 || expansions > maxCoverageExpansions {
			return nil, fmt.Errorf("coverage expansions must be a whole number between 0 and %d", maxCoverageExpansions)
		}

		options.MaxExpansions = expansions
	}

	if minNewValue != "" {
		minNew, err := strconv.Atoi(minNewValue)
		if err != nil || minNew < 0 || minNew > maxCoverageExpansionMinNew {
			return nil, fmt.Errorf("coverage expansion threshold must be a whole number between 0 and %d",
				maxCoverageExpansionMinNew)
		}

		options.ExpansionMinNew = minNew
	}

	return options, nil
}

// recordJobPlanWarning records that a plan artifact could not be materialised.
// The job itself is already saved and runnable, so this is evidence in the job
// monitor rather than a failed request. A repository without job events simply
// drops it.
func (s *Server) recordJobPlanWarning(ctx context.Context, jobID, artifact string, cause error) {
	_ = s.svc.RecordJobWorkerEvent(
		ctx, jobID, "collection-plan", "warning",
		fmt.Sprintf("Could not save the job's %s; the collection plan stays available through the API", artifact),
		map[string]any{"artifact": artifact, "error": cause.Error()},
	)
}

// wizardFieldSelection reads the step-3 data-field checkboxes. A complete
// selection, or none at all, stores nothing so the saved job definition stays
// byte-identical to one created before the step existed.
func wizardFieldSelection(r *http.Request) ([]string, error) {
	if r.Form == nil {
		return nil, nil
	}
	if strings.TrimSpace(r.FormValue("fields_selected")) != "on" {
		return nil, nil
	}

	fields, err := NormalizeJobFieldKeys(r.Form["fields"])
	if err != nil {
		return nil, err
	}

	return fields, nil
}

// wizardResultFilters reads the step-5 post-collection filters. Every value
// is optional; an empty step stores nothing.
func wizardResultFilters(r *http.Request) (*JobResultFilters, error) {
	ratingMin, err := optionalFormFloat(r, "filter_rating_min")
	if err != nil {
		return nil, err
	}
	ratingMax, err := optionalFormFloat(r, "filter_rating_max")
	if err != nil {
		return nil, err
	}
	reviewsMin, err := optionalFormInt64(r, "filter_reviews_min")
	if err != nil {
		return nil, err
	}
	reviewsMax, err := optionalFormInt64(r, "filter_reviews_max")
	if err != nil {
		return nil, err
	}

	filters := JobResultFilters{
		RatingMin: ratingMin, RatingMax: ratingMax,
		ReviewsMin: reviewsMin, ReviewsMax: reviewsMax,
		IncludeCategories: splitFilterList(r.FormValue("filter_include_categories")),
		ExcludeCategories: splitFilterList(r.FormValue("filter_exclude_categories")),
		NameContains:      strings.TrimSpace(r.FormValue("filter_name_contains")),
		NameExcludes:      strings.TrimSpace(r.FormValue("filter_name_excludes")),
	}
	if r.Form != nil {
		filters.Statuses = normalizeJobFilterList(r.Form["filter_status"])
	}
	switch strings.TrimSpace(r.FormValue("filter_claimed")) {
	case "claimed":
		claimed := true
		filters.Claimed = &claimed
	case "unclaimed":
		unclaimed := false
		filters.Claimed = &unclaimed
	case "", "any":
	default:
		return nil, fmt.Errorf("claim filter must be any, claimed, or unclaimed")
	}

	normalized := filters.Normalized()
	if normalized == nil {
		return nil, nil
	}
	if err := normalized.Validate(); err != nil {
		return nil, err
	}

	return normalized, nil
}

// splitFilterList accepts comma-separated or line-separated category lists.
func splitFilterList(raw string) []string {
	replaced := strings.ReplaceAll(raw, "\r\n", "\n")
	replaced = strings.ReplaceAll(replaced, "\r", "\n")
	replaced = strings.ReplaceAll(replaced, ",", "\n")

	return splitNonEmptyLines(replaced)
}

func optionalFormFloat(r *http.Request, name string) (*float64, error) {
	value := strings.TrimSpace(r.FormValue(name))
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return nil, fmt.Errorf("%s must be a number", strings.ReplaceAll(name, "_", " "))
	}

	return &parsed, nil
}

func optionalFormInt64(r *http.Request, name string) (*int64, error) {
	value := strings.TrimSpace(r.FormValue(name))
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%s must be a whole number", strings.ReplaceAll(name, "_", " "))
	}

	return &parsed, nil
}

func requiredFormInt(r *http.Request, name string) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(r.FormValue(name)))
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number", strings.ReplaceAll(name, "_", " "))
	}

	return value, nil
}

func optionalFormInt(r *http.Request, name string) (int, error) {
	value := strings.TrimSpace(r.FormValue(name))
	if value == "" {
		return 0, nil
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number", strings.ReplaceAll(name, "_", " "))
	}

	return parsed, nil
}

func optionalFormDuration(r *http.Request, name string) (time.Duration, error) {
	value := strings.TrimSpace(r.FormValue(name))
	if value == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration", strings.ReplaceAll(name, "_", " "))
	}
	return duration, nil
}

func radiusBoundingBox(latitude, longitude, radiusMetres float64) string {
	const earthRadiusMetres = 6371000.0

	angular := radiusMetres / earthRadiusMetres
	latDelta := angular * 180 / math.Pi
	cosine := math.Cos(latitude * math.Pi / 180)
	if math.Abs(cosine) < 1e-6 {
		cosine = math.Copysign(1e-6, cosine)
	}

	lonDelta := angular / math.Abs(cosine) * 180 / math.Pi
	minLat := max(-90, latitude-latDelta)
	maxLat := min(90, latitude+latDelta)
	minLon := max(-180, longitude-lonDelta)
	maxLon := min(180, longitude+lonDelta)

	return fmt.Sprintf("%.6f,%.6f,%.6f,%.6f", minLat, minLon, maxLat, maxLon)
}
