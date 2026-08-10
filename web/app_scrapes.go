package web

import (
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
		template := ScrapeTemplate{
			ID: uuid.NewString(), Name: name, Description: "Saved from the New Scrape wizard",
			Configuration: job.Data, CreatedAt: now, UpdatedAt: now,
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

	keywords, err := wizardKeywords(r)
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

	pagesBrowser, err := optionalFormInt(r, "pages_per_browser")
	if err != nil {
		return Job{}, "", err
	}

	maxTime, err := time.ParseDuration(strings.TrimSpace(r.FormValue("maxtime")))
	if err != nil || maxTime < 3*time.Minute {
		return Job{}, "", fmt.Errorf("maximum runtime must be a duration of at least 3m")
	}

	latitude := strings.TrimSpace(r.FormValue("latitude"))
	longitude := strings.TrimSpace(r.FormValue("longitude"))
	fastMode := r.FormValue("fastmode") == "on"

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
		Email:         r.FormValue("email") == "on",
		ExtraReviews:  r.FormValue("extra_reviews") == "on",
		MaxTime:       maxTime,
		Concurrency:   concurrency,
		BrowserPool:   browserPool,
		PagesBrowser:  pagesBrowser,
		ProxyPoolID:   strings.TrimSpace(r.FormValue("proxy_pool_id")),
	}
	if data.Lang == "" {
		data.Lang = "en"
	}

	if !fastMode {
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

func wizardKeywords(r *http.Request) ([]string, error) {
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

	if len(unique) == 0 {
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
