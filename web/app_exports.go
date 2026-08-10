package web

import (
	"bufio"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

const maximumExportRows = int64(1_000_000)

type exportsPageData struct {
	Query             string
	JobID             string
	Jobs              []resultJobOption
	Filters           []ResultFilter
	Sort              string
	IncludeDuplicates bool
	Exports           []exportHistoryView
	Notice            string
}

type exportHistoryView struct {
	ID          string
	Name        string
	Format      string
	State       string
	Rows        int64
	Size        string
	Checksum    string
	CreatedAt   string
	Error       string
	CanDownload bool
}

func (s *Server) exportsPage(w http.ResponseWriter, r *http.Request) {
	search, err := parseResultSearch(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	records, err := s.svc.ListExports(r.Context(), 100)
	if err != nil {
		http.Error(w, "could not load exports", http.StatusInternalServerError)
		return
	}
	jobs, err := s.svc.All(r.Context())
	if err != nil {
		http.Error(w, "could not load jobs", http.StatusInternalServerError)
		return
	}
	page := exportsPageData{
		Query:             strings.TrimSpace(r.URL.Query().Get("q")),
		JobID:             strings.TrimSpace(r.URL.Query().Get("job_id")),
		Filters:           search.Filters,
		Sort:              search.Sort,
		IncludeDuplicates: search.IncludeDuplicates,
		Notice:            strings.TrimSpace(r.URL.Query().Get("notice")),
	}
	for _, job := range jobs {
		page.Jobs = append(page.Jobs, resultJobOption{ID: job.ID, Name: job.Name, Selected: job.ID == page.JobID})
	}
	for _, record := range records {
		page.Exports = append(page.Exports, exportHistoryView{
			ID:          record.ID,
			Name:        record.Name,
			Format:      record.Format,
			State:       record.State,
			Rows:        record.RecordCount,
			Size:        humanBytes(record.FileSize),
			Checksum:    shortChecksum(record.Checksum),
			CreatedAt:   record.CreatedAt.Format(time.RFC3339),
			Error:       record.Error,
			CanDownload: record.State == "completed" && record.RelativePath != "",
		})
	}
	activity, _ := s.appActivity(r)
	s.renderAppPage(w, "exports", appPageData{
		Title:     "Exports",
		Subtitle:  "Create reproducible local files from normalized businesses and download them again later.",
		ActiveNav: "exports",
		Theme:     "system",
		CSRFToken: s.csrfToken,
		Activity:  activity,
		Page:      page,
	})
}

func (s *Server) createResultsExport(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.FormValue("format")))
	extension, ok := exportExtension(format)
	if !ok {
		http.Error(w, "unsupported export format", http.StatusUnprocessableEntity)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = "business-results"
	}
	if len(name) > 120 {
		http.Error(w, "export name is too long", http.StatusUnprocessableEntity)
		return
	}
	search, err := resultSearchFromForm(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	search.Limit = 250
	if search.Sort == "" {
		search.Sort = "updated_desc"
	}
	if len(search.Query) > maximumResultQueryLength || len(search.JobID) > 128 {
		http.Error(w, "export filter is too long", http.StatusUnprocessableEntity)
		return
	}
	filterJSON, _ := json.Marshal(search)
	now := time.Now().UTC()
	record := ExportRecord{
		ID:         uuid.NewString(),
		Name:       name,
		Format:     format,
		State:      "running",
		SourceType: "results",
		SourceID:   search.JobID,
		Filters:    string(filterJSON),
		Columns:    exportColumnsJSON(),
		CreatedAt:  now,
		StartedAt:  &now,
	}
	if err := s.svc.CreateExport(r.Context(), record); err != nil {
		renderLocalAPIError(w, http.StatusInternalServerError, "export_create_failed", "Could not register local export")
		return
	}

	relativePath := filepath.ToSlash(filepath.Join("exports", record.ID+"-"+exportSlug(name)+"."+extension))
	fullPath, err := safeDataPath(s.svc.dataFolder, relativePath)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(fullPath), 0o700)
	}
	var rowCount int64
	if err == nil {
		rowCount, err = s.writeResultExport(r, fullPath, format, search)
	}
	finished := time.Now().UTC()
	record.FinishedAt = &finished
	if err != nil {
		record.State = "failed"
		record.Error = publicExportError(err)
		if fullPath != "" {
			_ = os.Remove(fullPath)
		}
		_ = s.svc.UpdateExport(r.Context(), record)
		renderLocalAPIError(w, http.StatusInternalServerError, "export_failed", "Could not create the local export: "+record.Error)
		return
	}
	checksum, size, err := checksumLocalFile(fullPath)
	if err != nil {
		record.State = "failed"
		record.Error = "could not verify generated file"
		_ = os.Remove(fullPath)
		_ = s.svc.UpdateExport(r.Context(), record)
		renderLocalAPIError(w, http.StatusInternalServerError, "export_verify_failed", "Could not verify the local export")
		return
	}
	record.State = "completed"
	record.RelativePath = relativePath
	record.RecordCount = rowCount
	record.FileSize = size
	record.Checksum = checksum
	if err := s.svc.UpdateExport(r.Context(), record); err != nil {
		_ = os.Remove(fullPath)
		renderLocalAPIError(w, http.StatusInternalServerError, "export_register_failed", "Generated file could not be registered")
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		renderJSON(w, http.StatusCreated, localAPIEnvelope{Data: record})
		return
	}
	http.Redirect(w, r, "/app/exports?notice=Export+created+and+verified", http.StatusSeeOther)
}

func exportExtension(format string) (string, bool) {
	extensions := map[string]string{
		"csv": "csv", "json": "json", "jsonl": "jsonl", "geojson": "geojson",
		"kml": "kml", "vcard": "vcf", "txt": "txt",
		"postgresql_sql": "sql", "mysql_sql": "sql",
	}
	extension, ok := extensions[format]
	return extension, ok
}

func exportColumnsJSON() string {
	return "[\"name\",\"category\",\"address\",\"city\",\"state\",\"postal_code\",\"country\",\"latitude\",\"longitude\",\"phone\",\"email\",\"website\",\"domain\",\"rating\",\"review_count\",\"maps_url\",\"place_id\",\"cid\",\"data_id\",\"source_job_id\",\"source_query\",\"source_cell\",\"scraped_at\"]"
}

func exportSlug(value string) string {
	var builder strings.Builder
	lastDash := false
	for _, character := range strings.ToLower(value) {
		switch {
		case unicode.IsLetter(character), unicode.IsDigit(character):
			builder.WriteRune(character)
			lastDash = false
		case character == ' ', character == '-', character == '_':
			if builder.Len() > 0 && !lastDash {
				builder.WriteByte('-')
				lastDash = true
			}
		}
		if builder.Len() >= 48 {
			break
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if slug == "" {
		return "results"
	}
	return slug
}

func (s *Server) writeResultExport(r *http.Request, destination, format string, search ResultSearch) (int64, error) {
	tempPath := destination + ".tmp"
	file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	writer := bufio.NewWriterSize(file, 128*1024)
	rowCount, writeErr := s.streamExportRows(r, writer, format, search)
	if flushErr := writer.Flush(); writeErr == nil {
		writeErr = flushErr
	}
	if syncErr := file.Sync(); writeErr == nil {
		writeErr = syncErr
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(tempPath)
		return 0, writeErr
	}
	if err := os.Rename(tempPath, destination); err != nil {
		_ = os.Remove(tempPath)
		return 0, err
	}
	return rowCount, nil
}

func (s *Server) streamExportRows(r *http.Request, writer io.Writer, format string, search ResultSearch) (int64, error) {
	if err := writeExportStart(writer, format); err != nil {
		return 0, err
	}
	var count int64
	first := true
	for {
		search.Offset = int(count)
		page, err := s.svc.SearchBusinesses(r.Context(), search)
		if err != nil {
			return count, err
		}
		if page.Total > maximumExportRows {
			return count, fmt.Errorf("filter matches more than %d rows", maximumExportRows)
		}
		for _, business := range page.Results {
			if err := writeExportRow(writer, format, business, first); err != nil {
				return count, err
			}
			first = false
			count++
		}
		if len(page.Results) == 0 || count >= page.Total {
			break
		}
	}
	if err := writeExportEnd(writer, format); err != nil {
		return count, err
	}
	return count, nil
}

func writeExportStart(writer io.Writer, format string) error {
	switch format {
	case "csv":
		csvWriter := csv.NewWriter(writer)
		if err := csvWriter.Write(exportCSVHeader()); err != nil {
			return err
		}
		csvWriter.Flush()
		return csvWriter.Error()
	case "json":
		_, err := io.WriteString(writer, "[\n")
		return err
	case "geojson":
		_, err := io.WriteString(writer, "{\"type\":\"FeatureCollection\",\"features\":[\n")
		return err
	case "kml":
		_, err := io.WriteString(writer, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<kml xmlns=\"http://www.opengis.net/kml/2.2\"><Document>\n")
		return err
	case "postgresql_sql":
		_, err := io.WriteString(writer, "BEGIN;\n")
		return err
	case "mysql_sql":
		_, err := io.WriteString(writer, "START TRANSACTION;\n")
		return err
	default:
		return nil
	}
}

func writeExportEnd(writer io.Writer, format string) error {
	switch format {
	case "json":
		_, err := io.WriteString(writer, "\n]\n")
		return err
	case "geojson":
		_, err := io.WriteString(writer, "\n]}\n")
		return err
	case "kml":
		_, err := io.WriteString(writer, "</Document></kml>\n")
		return err
	case "postgresql_sql", "mysql_sql":
		_, err := io.WriteString(writer, "COMMIT;\n")
		return err
	default:
		return nil
	}
}

func writeExportRow(writer io.Writer, format string, business BusinessResult, first bool) error {
	switch format {
	case "csv":
		csvWriter := csv.NewWriter(writer)
		if err := csvWriter.Write(exportCSVRow(business)); err != nil {
			return err
		}
		csvWriter.Flush()
		return csvWriter.Error()
	case "json":
		if !first {
			if _, err := io.WriteString(writer, ",\n"); err != nil {
				return err
			}
		}
		data, err := json.Marshal(business)
		if err != nil {
			return err
		}
		_, err = writer.Write(data)
		return err
	case "jsonl":
		return json.NewEncoder(writer).Encode(business)
	case "geojson":
		if !first {
			if _, err := io.WriteString(writer, ",\n"); err != nil {
				return err
			}
		}
		feature := map[string]any{"type": "Feature", "geometry": nil, "properties": business}
		if business.Latitude != nil && business.Longitude != nil {
			feature["geometry"] = map[string]any{
				"type": "Point", "coordinates": []float64{*business.Longitude, *business.Latitude},
			}
		}
		data, err := json.Marshal(feature)
		if err != nil {
			return err
		}
		_, err = writer.Write(data)
		return err
	case "kml":
		return writeKMLBusiness(writer, business)
	case "vcard":
		_, err := fmt.Fprintf(writer, "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:%s\r\nORG:%s\r\nTEL:%s\r\nEMAIL:%s\r\nURL:%s\r\nADR:;;%s;;;;\r\nEND:VCARD\r\n",
			vcardEscape(business.Name), vcardEscape(business.Name), vcardEscape(business.Phone),
			vcardEscape(business.PrimaryEmail), vcardEscape(business.Website), vcardEscape(business.Address))
		return err
	case "txt":
		_, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", business.Name, business.Domain,
			business.PrimaryEmail, business.Phone, business.Address)
		return err
	case "postgresql_sql", "mysql_sql":
		_, err := fmt.Fprintf(writer,
			"INSERT INTO businesses (id,name,category,address,phone,email,website,domain,latitude,longitude,rating,review_count,scraped_at) VALUES (%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s);\n",
			sqlExportValue(business.ID), sqlExportValue(business.Name), sqlExportValue(business.PrimaryCategory),
			sqlExportValue(business.Address), sqlExportValue(business.Phone), sqlExportValue(business.PrimaryEmail),
			sqlExportValue(business.Website), sqlExportValue(business.Domain), floatPointerSQL(business.Latitude),
			floatPointerSQL(business.Longitude), floatPointerSQL(business.Rating), intPointerSQL(business.ReviewCount),
			sqlExportValue(business.ScrapedAt.Format(time.RFC3339)))
		return err
	default:
		return fmt.Errorf("unsupported export format")
	}
}

func exportCSVHeader() []string {
	return []string{"id", "name", "category", "address", "city", "state", "postal_code", "country",
		"latitude", "longitude", "phone", "email", "website", "domain", "rating", "review_count",
		"maps_url", "place_id", "cid", "data_id", "source_job_id", "source_query", "source_cell", "scraped_at"}
}

func exportCSVRow(b BusinessResult) []string {
	return []string{b.ID, b.Name, b.PrimaryCategory, b.Address, b.City, b.State, b.PostalCode, b.Country,
		floatPointerString(b.Latitude), floatPointerString(b.Longitude), b.Phone, b.PrimaryEmail, b.Website, b.Domain,
		floatPointerString(b.Rating), intPointerString(b.ReviewCount), b.MapsURL, b.PlaceID, b.CID, b.DataID,
		b.SourceJobID, b.SourceQuery, b.SourceCell, b.ScrapedAt.Format(time.RFC3339)}
}

func floatPointerString(value *float64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatFloat(*value, 'f', -1, 64)
}

func intPointerString(value *int64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(*value, 10)
}

func sqlExportValue(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func floatPointerSQL(value *float64) string {
	if value == nil {
		return "NULL"
	}
	return strconv.FormatFloat(*value, 'f', -1, 64)
}

func intPointerSQL(value *int64) string {
	if value == nil {
		return "NULL"
	}
	return strconv.FormatInt(*value, 10)
}

func writeKMLBusiness(writer io.Writer, business BusinessResult) error {
	if business.Latitude == nil || business.Longitude == nil {
		return nil
	}
	if _, err := io.WriteString(writer, "<Placemark><name>"); err != nil {
		return err
	}
	if err := xml.EscapeText(writer, []byte(business.Name)); err != nil {
		return err
	}
	if _, err := io.WriteString(writer, "</name><description>"); err != nil {
		return err
	}
	if err := xml.EscapeText(writer, []byte(business.Address)); err != nil {
		return err
	}
	_, err := fmt.Fprintf(writer, "</description><Point><coordinates>%s,%s</coordinates></Point></Placemark>\n",
		strconv.FormatFloat(*business.Longitude, 'f', -1, 64),
		strconv.FormatFloat(*business.Latitude, 'f', -1, 64))
	return err
}

func vcardEscape(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", "\\n")
	value = strings.ReplaceAll(value, ";", "\\;")
	value = strings.ReplaceAll(value, ",", "\\,")
	return value
}

func checksumLocalFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func publicExportError(err error) string {
	if strings.Contains(err.Error(), "more than") {
		return err.Error()
	}
	return "file generation failed"
}

func (s *Server) downloadExport(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !validBusinessID(id) {
		http.Error(w, "invalid export id", http.StatusUnprocessableEntity)
		return
	}
	record, path, err := s.svc.GetExport(r.Context(), id)
	if err != nil {
		http.Error(w, "export not found", http.StatusNotFound)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.Error(w, "export file unavailable", http.StatusNotFound)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", exportContentType(record.Format))
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filepath.Base(path)+"\"")
	w.Header().Set("X-Checksum-SHA256", record.Checksum)
	_, _ = io.Copy(w, file)
}

func exportContentType(format string) string {
	switch format {
	case "csv":
		return "text/csv; charset=utf-8"
	case "json", "geojson":
		return "application/json"
	case "jsonl":
		return "application/x-ndjson"
	case "kml":
		return "application/vnd.google-earth.kml+xml"
	case "vcard":
		return "text/vcard; charset=utf-8"
	default:
		return "text/plain; charset=utf-8"
	}
}

func (s *Server) deleteExport(w http.ResponseWriter, r *http.Request) {
	if !s.requireCSRF(w, r) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if !validBusinessID(id) {
		http.Error(w, "invalid export id", http.StatusUnprocessableEntity)
		return
	}
	if err := s.svc.DeleteExport(r.Context(), id); err != nil {
		if errors.Is(err, ErrExportNotFound) {
			http.Error(w, "export not found", http.StatusNotFound)
		} else {
			http.Error(w, "could not delete export", http.StatusInternalServerError)
		}
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		renderJSON(w, http.StatusOK, localAPIEnvelope{Data: map[string]string{"message": "Export deleted"}})
		return
	}
	http.Redirect(w, r, "/app/exports?notice=Export+deleted", http.StatusSeeOther)
}
