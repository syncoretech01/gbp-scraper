package web

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gosom/google-maps-scraper/web/prospect"
)

const (
	maximumExportColumns     = 64
	maximumExportParts       = 2048
	maximumSelectedExportIDs = 5000
	maximumSpoolRowBytes     = 32 << 20
	maximumOpenSpools        = 16
	defaultSplitRows         = int64(50_000)
	maximumXLSXDataRows      = int64(1_048_575)
)

// ExportColumnSelection is one validated output column. Key identifies a
// known local field while Label is the user-controlled exported heading.
type ExportColumnSelection struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// ExportBuildOptions controls a bounded, repeatable export generation run.
type ExportBuildOptions struct {
	SplitBy           string `json:"split_by,omitempty"`
	MaxRowsPerFile    int64  `json:"max_rows_per_file,omitempty"`
	ZIP               bool   `json:"zip,omitempty"`
	IncludeRaw        bool   `json:"include_raw,omitempty"`
	IncludeSources    bool   `json:"include_sources,omitempty"`
	IncludeProvenance bool   `json:"include_provenance,omitempty"`
	IncludeChanges    bool   `json:"include_changes,omitempty"`
	Deduplicate       bool   `json:"deduplicate"`
	LegacyShape       bool   `json:"legacy_shape,omitempty"`
}

type exportColumnDefinition struct {
	Key      string
	Label    string
	DataType string
}

var exportColumnDefinitions = []exportColumnDefinition{
	{Key: "id", Label: "id", DataType: "text"},
	{Key: "name", Label: "name", DataType: "text"},
	{Key: "category", Label: "category", DataType: "text"},
	{Key: "additional_categories", Label: "additional_categories", DataType: "text"},
	{Key: "business_status", Label: "business_status", DataType: "text"},
	{Key: "claimed", Label: "claimed", DataType: "boolean"},
	{Key: "address", Label: "address", DataType: "text"},
	{Key: "city", Label: "city", DataType: "text"},
	{Key: "state", Label: "state", DataType: "text"},
	{Key: "postal_code", Label: "postal_code", DataType: "text"},
	{Key: "country", Label: "country", DataType: "text"},
	{Key: "latitude", Label: "latitude", DataType: "number"},
	{Key: "longitude", Label: "longitude", DataType: "number"},
	{Key: "phone", Label: "phone", DataType: "text"},
	{Key: "normalized_phone", Label: "normalized_phone", DataType: "text"},
	{Key: "email", Label: "email", DataType: "text"},
	{Key: "website", Label: "website", DataType: "text"},
	{Key: "domain", Label: "domain", DataType: "text"},
	{Key: "website_status", Label: "website_status", DataType: "text"},
	{Key: "website_response_ms", Label: "website_response_ms", DataType: "integer"},
	{Key: "rating", Label: "rating", DataType: "number"},
	{Key: "review_count", Label: "review_count", DataType: "integer"},
	{Key: "quality_score", Label: "quality_score", DataType: "number"},
	{Key: "confidence", Label: "confidence", DataType: "number"},
	{Key: "reviewed", Label: "reviewed", DataType: "boolean"},
	{Key: "notes", Label: "notes", DataType: "text"},
	{Key: "change_status", Label: "change_status", DataType: "text"},
	{Key: "tags", Label: "tags", DataType: "json"},
	{Key: "maps_url", Label: "maps_url", DataType: "text"},
	{Key: "place_id", Label: "place_id", DataType: "text"},
	{Key: "cid", Label: "cid", DataType: "text"},
	{Key: "data_id", Label: "data_id", DataType: "text"},
	{Key: "source_job_id", Label: "source_job_id", DataType: "text"},
	{Key: "source_query", Label: "source_query", DataType: "text"},
	{Key: "source_cell", Label: "source_cell", DataType: "text"},
	{Key: "scraped_at", Label: "scraped_at", DataType: "datetime"},
	{Key: "updated_at", Label: "updated_at", DataType: "datetime"},
	{Key: "raw_json", Label: "raw_json", DataType: "json"},
	{Key: "sources_json", Label: "sources_json", DataType: "json"},
	{Key: "provenance_json", Label: "provenance_json", DataType: "json"},
	{Key: "changes_json", Label: "changes_json", DataType: "json"},
	{Key: "prospect_status", Label: "prospect_status", DataType: "text"},
	{Key: "prospect_score", Label: "prospect_score", DataType: "number"},
	{Key: "prospect_tier", Label: "prospect_tier", DataType: "text"},
	{Key: "prospect_reasons", Label: "prospect_reasons", DataType: "json"},
	{Key: "call_opener", Label: "call_opener", DataType: "text"},
	// Specification section 08 core columns that the normalized row now
	// carries. Every one is additive: no default export changes shape.
	{Key: "description", Label: "description", DataType: "text"},
	{Key: "street", Label: "street", DataType: "text"},
	{Key: "plus_code", Label: "plus_code", DataType: "text"},
	{Key: "input_id", Label: "input_id", DataType: "text"},
	{Key: "phone_type", Label: "phone_type", DataType: "text"},
	{Key: "emails", Label: "emails", DataType: "text"},
	{Key: "email_type", Label: "email_type", DataType: "text"},
	{Key: "email_status", Label: "email_status", DataType: "text"},
	{Key: "facebook", Label: "facebook", DataType: "text"},
	{Key: "instagram", Label: "instagram", DataType: "text"},
	{Key: "linkedin", Label: "linkedin", DataType: "text"},
	{Key: "x", Label: "x", DataType: "text"},
	{Key: "youtube", Label: "youtube", DataType: "text"},
	{Key: "tiktok", Label: "tiktok", DataType: "text"},
	{Key: "whatsapp", Label: "whatsapp", DataType: "text"},
	{Key: "reviews_per_rating", Label: "reviews_per_rating", DataType: "text"},
	{Key: "user_reviews", Label: "user_reviews", DataType: "text"},
	{Key: "popular_times", Label: "popular_times", DataType: "text"},
	{Key: "technologies", Label: "technologies", DataType: "text"},
	{Key: "last_checked_at", Label: "last_checked_at", DataType: "datetime"},
	{Key: "first_seen_at", Label: "first_seen_at", DataType: "datetime"},
	{Key: "last_seen_at", Label: "last_seen_at", DataType: "datetime"},
}

var defaultExportColumnKeys = []string{
	"id", "name", "category", "address", "city", "state", "postal_code", "country",
	"latitude", "longitude", "phone", "email", "website", "domain", "rating", "review_count",
	"maps_url", "place_id", "cid", "data_id", "source_job_id", "source_query", "source_cell", "scraped_at",
}

func defaultExportColumns() []ExportColumnSelection {
	columns := make([]ExportColumnSelection, 0, len(defaultExportColumnKeys))
	for _, key := range defaultExportColumnKeys {
		columns = append(columns, ExportColumnSelection{Key: key, Label: key})
	}
	return columns
}

func defaultExportColumnSpec() string {
	lines := make([]string, 0, len(defaultExportColumnKeys))
	for _, key := range defaultExportColumnKeys {
		lines = append(lines, key+"="+key)
	}
	return strings.Join(lines, "\n")
}

func availableExportColumnOptions() []ExportColumnSelection {
	columns := make([]ExportColumnSelection, 0, len(exportColumnDefinitions)-4)
	for _, definition := range exportColumnDefinitions {
		if strings.HasSuffix(definition.Key, "_json") || definition.Key == "raw_json" {
			continue
		}
		columns = append(columns, ExportColumnSelection{Key: definition.Key, Label: definition.Label})
	}
	return columns
}

func exportColumnDefinitionFor(key string) (exportColumnDefinition, bool) {
	for _, definition := range exportColumnDefinitions {
		if definition.Key == key {
			return definition, true
		}
	}
	return exportColumnDefinition{}, false
}

func parseExportColumnSpec(raw string, options ExportBuildOptions) ([]ExportColumnSelection, bool, error) {
	raw = strings.TrimSpace(raw)
	legacyShape := raw == ""
	if raw == "" {
		raw = defaultExportColumnSpec()
	}
	if !strings.Contains(raw, "\n") && strings.Contains(raw, ",") && !strings.Contains(raw, "=") {
		raw = strings.ReplaceAll(raw, ",", "\n")
	}
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	columns := make([]ExportColumnSelection, 0, len(lines)+4)
	seen := make(map[string]struct{}, len(lines)+4)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, label, found := strings.Cut(line, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		if !found {
			label = key
		}
		label = strings.TrimSpace(label)
		if _, ok := exportColumnDefinitionFor(key); !ok || strings.HasSuffix(key, "_json") || key == "raw_json" {
			return nil, false, fmt.Errorf("unsupported export column %q", key)
		}
		if _, exists := seen[key]; exists {
			return nil, false, fmt.Errorf("export column %q is repeated", key)
		}
		if err := validateExportColumnLabel(label); err != nil {
			return nil, false, err
		}
		seen[key] = struct{}{}
		columns = append(columns, ExportColumnSelection{Key: key, Label: label})
	}
	detailColumns := []struct {
		include bool
		key     string
	}{
		{include: options.IncludeRaw, key: "raw_json"},
		{include: options.IncludeSources, key: "sources_json"},
		{include: options.IncludeProvenance, key: "provenance_json"},
		{include: options.IncludeChanges, key: "changes_json"},
	}
	for _, detail := range detailColumns {
		if !detail.include {
			continue
		}
		if _, exists := seen[detail.key]; exists {
			continue
		}
		seen[detail.key] = struct{}{}
		columns = append(columns, ExportColumnSelection{Key: detail.key, Label: detail.key})
		legacyShape = false
	}
	if len(columns) == 0 {
		return nil, false, fmt.Errorf("at least one export column is required")
	}
	if len(columns) > maximumExportColumns {
		return nil, false, fmt.Errorf("at most %d export columns are allowed", maximumExportColumns)
	}
	legacyShape = equalExportColumns(columns, defaultExportColumns()) &&
		!options.IncludeRaw && !options.IncludeSources && !options.IncludeProvenance && !options.IncludeChanges
	return columns, legacyShape, nil
}

func validateExportColumnLabel(label string) error {
	if label == "" || utf8.RuneCountInString(label) > 80 {
		return fmt.Errorf("export column labels must contain 1 to 80 characters")
	}
	for _, character := range label {
		if unicode.IsControl(character) {
			return fmt.Errorf("export column labels cannot contain control characters")
		}
	}
	return nil
}

func equalExportColumns(left, right []ExportColumnSelection) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func decodeExportColumns(raw string) ([]ExportColumnSelection, error) {
	var columns []ExportColumnSelection
	if err := json.Unmarshal([]byte(raw), &columns); err != nil {
		// Original installations stored a string array. Accept it indefinitely.
		var legacy []string
		if legacyErr := json.Unmarshal([]byte(raw), &legacy); legacyErr != nil {
			return nil, fmt.Errorf("decode export columns: %w", err)
		}
		for _, key := range legacy {
			columns = append(columns, ExportColumnSelection{Key: key, Label: key})
		}
	}
	if len(columns) == 0 || len(columns) > maximumExportColumns {
		return nil, fmt.Errorf("stored export columns are invalid")
	}
	seen := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		if _, ok := exportColumnDefinitionFor(column.Key); !ok {
			return nil, fmt.Errorf("stored export column %q is unsupported", column.Key)
		}
		if _, exists := seen[column.Key]; exists {
			return nil, fmt.Errorf("stored export column %q is repeated", column.Key)
		}
		if err := validateExportColumnLabel(column.Label); err != nil {
			return nil, err
		}
		seen[column.Key] = struct{}{}
	}
	return columns, nil
}

func parseExportBuildOptions(r formValueReader) (ExportBuildOptions, error) {
	options := ExportBuildOptions{
		SplitBy:           strings.ToLower(strings.TrimSpace(r.FormValue("split_by"))),
		ZIP:               formBoolean(r, "zip", false),
		IncludeRaw:        formBoolean(r, "include_raw", false),
		IncludeSources:    formBoolean(r, "include_sources", false),
		IncludeProvenance: formBoolean(r, "include_provenance", false),
		IncludeChanges:    formBoolean(r, "include_changes", false),
		Deduplicate:       formBoolean(r, "deduplicate", true),
	}
	switch options.SplitBy {
	case "", "none":
		options.SplitBy = "none"
	case "city", "category", "job", "max_rows":
	default:
		return ExportBuildOptions{}, fmt.Errorf("unsupported export split mode")
	}
	if raw := strings.TrimSpace(r.FormValue("max_rows_per_file")); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 1 || value > maximumExportRows {
			return ExportBuildOptions{}, fmt.Errorf("maximum rows per file must be between 1 and %d", maximumExportRows)
		}
		options.MaxRowsPerFile = value
	}
	if options.SplitBy == "max_rows" && options.MaxRowsPerFile == 0 {
		options.MaxRowsPerFile = defaultSplitRows
	}
	return options, nil
}

type formValueReader interface {
	FormValue(string) string
}

// parseBoundedRequestForm reads a bounded HTML form body. Browsers submit
// application/x-www-form-urlencoded unless the form declares a file input, and
// http.Request.ParseMultipartForm rejects that encoding with ErrNotMultipart, so
// the encoding must decide which parser runs.
func parseBoundedRequestForm(w http.ResponseWriter, r *http.Request, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit*2)
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		return r.ParseMultipartForm(limit)
	}

	return r.ParseForm()
}

func formBoolean(reader formValueReader, name string, fallback bool) bool {
	raw := strings.ToLower(strings.TrimSpace(reader.FormValue(name)))
	if raw == "" {
		return fallback
	}
	return raw == "1" || raw == "true" || raw == "yes" || raw == "on"
}

func encodeExportConfiguration(columns []ExportColumnSelection, options ExportBuildOptions) (string, string, error) {
	columnJSON, err := json.Marshal(columns)
	if err != nil {
		return "", "", fmt.Errorf("encode export columns: %w", err)
	}
	optionJSON, err := json.Marshal(options)
	if err != nil {
		return "", "", fmt.Errorf("encode export options: %w", err)
	}
	return string(columnJSON), string(optionJSON), nil
}

func decodeExportOptions(raw string) (ExportBuildOptions, error) {
	options := ExportBuildOptions{SplitBy: "none", Deduplicate: true}
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "{}" {
		return options, nil
	}
	if err := json.Unmarshal([]byte(raw), &options); err != nil {
		return ExportBuildOptions{}, fmt.Errorf("decode export options: %w", err)
	}
	switch options.SplitBy {
	case "", "none":
		options.SplitBy = "none"
	case "city", "category", "job", "max_rows":
	default:
		return ExportBuildOptions{}, fmt.Errorf("stored export split mode is invalid")
	}
	if options.MaxRowsPerFile < 0 || options.MaxRowsPerFile > maximumExportRows {
		return ExportBuildOptions{}, fmt.Errorf("stored export row limit is invalid")
	}
	if options.SplitBy == "max_rows" && options.MaxRowsPerFile == 0 {
		options.MaxRowsPerFile = defaultSplitRows
	}
	return options, nil
}

type exportDataRow struct {
	Business BusinessResult      `json:"business"`
	Detail   *BusinessDetail     `json:"detail,omitempty"`
	Prospect *prospectExportData `json:"prospect,omitempty"`
}

// exportProspectValue returns the prospect data spooled with the row, or
// computes it from the built-in defaults for rows spooled before prospect
// support (and for writers driven directly in tests).
func exportProspectValue(row exportDataRow) prospectExportData {
	if row.Prospect != nil {
		return *row.Prospect
	}
	return computeProspectExportData(row.Business, prospect.DefaultScoreWeights(), prospect.DefaultOpenerTemplates())
}

type exportSpoolGroup struct {
	Key       string
	Label     string
	Path      string
	Rows      int64
	Ordinal   int
	lastTouch uint64
	file      *os.File
	writer    *bufio.Writer
}

type exportArtifact struct {
	RelativePath string
	RecordCount  int64
	FileSize     int64
	Checksum     string
	Parts        []ExportPart
}

type exportPartWriter interface {
	Add(exportDataRow) error
	Close() error
	Abort()
}

func (s *Server) generateConfiguredExport(
	ctx context.Context,
	record ExportRecord,
	search ResultSearch,
	columns []ExportColumnSelection,
	options ExportBuildOptions,
) (artifact exportArtifact, err error) {
	exportsDirectory, err := safeDataPath(s.svc.dataFolder, "exports")
	if err != nil {
		return exportArtifact{}, err
	}
	if err := os.MkdirAll(exportsDirectory, 0o700); err != nil {
		return exportArtifact{}, fmt.Errorf("create export directory: %w", err)
	}
	workDirectory, err := os.MkdirTemp(exportsDirectory, ".work-"+record.ID+"-")
	if err != nil {
		return exportArtifact{}, fmt.Errorf("create export workspace: %w", err)
	}
	defer os.RemoveAll(workDirectory)

	groups, rowCount, err := s.spoolExportRows(ctx, workDirectory, search, options)
	if err != nil {
		return exportArtifact{}, err
	}
	if record.Format == "xlsx" {
		for _, group := range groups {
			if group.Rows > maximumXLSXDataRows {
				return exportArtifact{}, fmt.Errorf("an XLSX part contains more than %d data rows; split by maximum rows", maximumXLSXDataRows)
			}
		}
	}

	createdPaths := make([]string, 0, len(groups)+1)
	defer func() {
		if err == nil {
			return
		}
		for _, path := range createdPaths {
			_ = os.Remove(path)
		}
	}()

	extension, _ := exportExtension(record.Format)
	baseName := record.ID + "-" + exportSlug(record.Name)
	sort.SliceStable(groups, func(left, right int) bool {
		if options.SplitBy == "max_rows" {
			return groups[left].Ordinal < groups[right].Ordinal
		}
		return strings.ToLower(groups[left].Label) < strings.ToLower(groups[right].Label)
	})
	parts := make([]ExportPart, 0, len(groups))
	for index, group := range groups {
		if err := ctx.Err(); err != nil {
			return exportArtifact{}, err
		}
		fileName := baseName + "." + extension
		if len(groups) > 1 || options.ZIP {
			fileName = fmt.Sprintf("%s-part-%04d-%s.%s", baseName, index+1, exportSlug(group.Label), extension)
		}
		destination := filepath.Join(exportsDirectory, fileName)
		writer, err := newExportPartWriter(destination, record.Format, columns, options)
		if err != nil {
			return exportArtifact{}, err
		}
		createdPaths = append(createdPaths, destination)
		if err := copyExportSpool(ctx, group.Path, writer); err != nil {
			writer.Abort()
			return exportArtifact{}, err
		}
		if err := writer.Close(); err != nil {
			return exportArtifact{}, err
		}
		if err := verifyExportFile(record.Format, destination); err != nil {
			return exportArtifact{}, fmt.Errorf("verify export part %d: %w", index+1, err)
		}
		checksum, size, err := checksumLocalFile(destination)
		if err != nil {
			return exportArtifact{}, fmt.Errorf("checksum export part %d: %w", index+1, err)
		}
		relative := filepath.ToSlash(filepath.Join("exports", fileName))
		parts = append(parts, ExportPart{
			ExportID: record.ID, PartNumber: index + 1, RelativePath: relative,
			RecordCount: group.Rows, FileSize: size, Checksum: checksum,
		})
	}

	mainPath := createdPaths[0]
	mainRelative := parts[0].RelativePath
	if len(parts) > 1 || options.ZIP {
		archiveName := baseName + ".zip"
		mainPath = filepath.Join(exportsDirectory, archiveName)
		if err := writeExportZIP(mainPath, createdPaths); err != nil {
			return exportArtifact{}, err
		}
		createdPaths = append(createdPaths, mainPath)
		mainRelative = filepath.ToSlash(filepath.Join("exports", archiveName))
		if err := verifyZIP(mainPath, len(parts)); err != nil {
			return exportArtifact{}, fmt.Errorf("verify export archive: %w", err)
		}
	}
	checksum, size, err := checksumLocalFile(mainPath)
	if err != nil {
		return exportArtifact{}, fmt.Errorf("checksum export: %w", err)
	}
	return exportArtifact{
		RelativePath: mainRelative, RecordCount: rowCount, FileSize: size,
		Checksum: checksum, Parts: parts,
	}, nil
}

func (s *Server) spoolExportRows(
	ctx context.Context,
	workDirectory string,
	search ResultSearch,
	options ExportBuildOptions,
) ([]*exportSpoolGroup, int64, error) {
	search.Limit = 250
	search.Offset = 0
	groupsByKey := make(map[string]*exportSpoolGroup)
	groups := make([]*exportSpoolGroup, 0, 8)
	var rowCount int64
	var touch uint64
	prospectWeights, prospectTemplates := s.svc.prospectExportConfiguration(ctx)
	closeSpools := func() error {
		var closeErrors []error
		for _, group := range groups {
			if err := closeExportSpool(group); err != nil {
				closeErrors = append(closeErrors, err)
			}
		}
		return errors.Join(closeErrors...)
	}
	defer closeSpools()

	for {
		if err := ctx.Err(); err != nil {
			return nil, rowCount, err
		}
		search.Offset = int(rowCount)
		page, err := s.svc.SearchBusinesses(ctx, search)
		if err != nil {
			return nil, rowCount, err
		}
		if page.Total > maximumExportRows {
			return nil, rowCount, fmt.Errorf("filter matches more than %d rows", maximumExportRows)
		}
		for _, business := range page.Results {
			data := exportDataRow{Business: business}
			computed := computeProspectExportData(business, prospectWeights, prospectTemplates)
			data.Prospect = &computed
			if options.IncludeRaw || options.IncludeSources || options.IncludeProvenance || options.IncludeChanges {
				detail, err := s.svc.GetBusiness(ctx, business.ID)
				if err != nil {
					return nil, rowCount, fmt.Errorf("load optional export detail for %s: %w", business.ID, err)
				}
				data.Detail = &detail
			}
			key, label := exportGroupFor(data.Business, options, rowCount)
			group := groupsByKey[key]
			if group == nil {
				if len(groups) >= maximumExportParts {
					return nil, rowCount, fmt.Errorf("split produces more than %d files", maximumExportParts)
				}
				group = &exportSpoolGroup{
					Key: key, Label: label, Ordinal: len(groups),
					Path: filepath.Join(workDirectory, fmt.Sprintf("spool-%04d.jsonl", len(groups)+1)),
				}
				groupsByKey[key] = group
				groups = append(groups, group)
			}
			touch++
			if group.writer == nil {
				if err := openExportSpool(groups, group, touch); err != nil {
					return nil, rowCount, err
				}
			}
			encoded, err := json.Marshal(data)
			if err != nil {
				return nil, rowCount, fmt.Errorf("encode export spool row: %w", err)
			}
			if len(encoded) > maximumSpoolRowBytes {
				return nil, rowCount, fmt.Errorf("business %s exceeds the bounded export row size", business.ID)
			}
			if _, err := group.writer.Write(encoded); err != nil {
				return nil, rowCount, fmt.Errorf("write export spool: %w", err)
			}
			if err := group.writer.WriteByte('\n'); err != nil {
				return nil, rowCount, fmt.Errorf("write export spool: %w", err)
			}
			group.Rows++
			group.lastTouch = touch
			rowCount++
		}
		if len(page.Results) == 0 || rowCount >= page.Total {
			break
		}
	}
	if len(groups) == 0 {
		group := &exportSpoolGroup{Key: "results", Label: "results", Ordinal: 0, Path: filepath.Join(workDirectory, "spool-0001.jsonl")}
		if err := os.WriteFile(group.Path, nil, 0o600); err != nil {
			return nil, 0, fmt.Errorf("create empty export spool: %w", err)
		}
		groups = append(groups, group)
	}
	if err := closeSpools(); err != nil {
		return nil, rowCount, err
	}
	return groups, rowCount, nil
}

func exportGroupFor(business BusinessResult, options ExportBuildOptions, rowIndex int64) (string, string) {
	value := "results"
	switch options.SplitBy {
	case "city":
		value = strings.TrimSpace(business.City)
	case "category":
		value = strings.TrimSpace(business.PrimaryCategory)
	case "job":
		value = strings.TrimSpace(business.SourceJobID)
	case "max_rows":
		part := rowIndex/options.MaxRowsPerFile + 1
		return fmt.Sprintf("rows-%08d", part), fmt.Sprintf("rows-%04d", part)
	}
	if value == "" {
		value = "unknown"
	}
	return options.SplitBy + ":" + strings.ToLower(value), value
}

func openExportSpool(groups []*exportSpoolGroup, target *exportSpoolGroup, touch uint64) error {
	openCount := 0
	var oldest *exportSpoolGroup
	for _, group := range groups {
		if group.writer == nil {
			continue
		}
		openCount++
		if oldest == nil || group.lastTouch < oldest.lastTouch {
			oldest = group
		}
	}
	if openCount >= maximumOpenSpools && oldest != nil {
		if err := closeExportSpool(oldest); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(target.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open export spool: %w", err)
	}
	target.file = file
	target.writer = bufio.NewWriterSize(file, 64*1024)
	target.lastTouch = touch
	return nil
}

func closeExportSpool(group *exportSpoolGroup) error {
	if group.writer == nil {
		return nil
	}
	flushErr := group.writer.Flush()
	closeErr := group.file.Close()
	group.writer = nil
	group.file = nil
	if flushErr != nil {
		return fmt.Errorf("flush export spool: %w", flushErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close export spool: %w", closeErr)
	}
	return nil
}

func copyExportSpool(ctx context.Context, path string, writer exportPartWriter) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open export spool: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(bufio.NewReaderSize(file, 128*1024))
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var row exportDataRow
		err := decoder.Decode(&row)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("decode export spool: %w", err)
		}
		if err := writer.Add(row); err != nil {
			return err
		}
	}
}

func newExportPartWriter(
	destination, format string,
	columns []ExportColumnSelection,
	options ExportBuildOptions,
) (exportPartWriter, error) {
	switch format {
	case "xlsx":
		return newXLSXExportWriter(destination, columns)
	case "sqlite":
		return newSQLiteExportWriter(destination, columns, options)
	case "parquet":
		return newParquetExportWriter(destination, columns, options)
	case "discovered_companies":
		return newDiscoveredCompaniesExportWriter(destination)
	default:
		return newTextExportWriter(destination, format, columns, options)
	}
}

func writeExportZIP(destination string, paths []string) error {
	temporary := destination + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create export archive: %w", err)
	}
	archive := zip.NewWriter(file)
	writeErr := error(nil)
	for _, path := range paths {
		if writeErr != nil {
			break
		}
		source, openErr := os.Open(path)
		if openErr != nil {
			writeErr = openErr
			break
		}
		entry, createErr := archive.CreateHeader(&zip.FileHeader{Name: filepath.Base(path), Method: zip.Deflate})
		if createErr == nil {
			_, createErr = io.Copy(entry, source)
		}
		closeErr := source.Close()
		if createErr != nil {
			writeErr = createErr
		} else if closeErr != nil {
			writeErr = closeErr
		}
	}
	if closeErr := archive.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if syncErr := file.Sync(); writeErr == nil {
		writeErr = syncErr
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("write export archive: %w", writeErr)
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("publish export archive: %w", err)
	}
	return nil
}

func verifyZIP(path string, expectedFiles int) error {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer archive.Close()
	if len(archive.File) != expectedFiles {
		return fmt.Errorf("archive contains %d files, expected %d", len(archive.File), expectedFiles)
	}
	for _, file := range archive.File {
		if file.FileInfo().IsDir() || filepath.Base(file.Name) != file.Name {
			return fmt.Errorf("archive contains an unsafe member name")
		}
	}
	return nil
}

func verifyExportFile(format, path string) error {
	switch format {
	case "xlsx":
		return verifyXLSX(path)
	case "sqlite":
		return verifySQLiteExport(path)
	case "parquet":
		return verifyParquetExport(path)
	case "discovered_companies":
		return verifyDiscoveredCompaniesExport(path)
	default:
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("generated export is not a regular file")
		}
		return nil
	}
}

func exportColumnValue(row exportDataRow, key string) (any, error) {
	business := row.Business
	switch key {
	case "id":
		return business.ID, nil
	case "name":
		return business.Name, nil
	case "category":
		return business.PrimaryCategory, nil
	case "additional_categories":
		return business.AdditionalCategories, nil
	case "business_status":
		return business.BusinessStatus, nil
	case "claimed":
		return business.Claimed, nil
	case "address":
		return business.Address, nil
	case "city":
		return business.City, nil
	case "state":
		return business.State, nil
	case "postal_code":
		return business.PostalCode, nil
	case "country":
		return business.Country, nil
	case "latitude":
		return pointerValue(business.Latitude), nil
	case "longitude":
		return pointerValue(business.Longitude), nil
	case "phone":
		return business.Phone, nil
	case "normalized_phone":
		return business.NormalizedPhone, nil
	case "email":
		return business.PrimaryEmail, nil
	case "website":
		return business.Website, nil
	case "domain":
		return business.Domain, nil
	case "website_status":
		return business.WebsiteStatus, nil
	case "website_response_ms":
		return pointerValue(business.WebsiteResponseMS), nil
	case "rating":
		return pointerValue(business.Rating), nil
	case "review_count":
		return pointerValue(business.ReviewCount), nil
	case "quality_score":
		return business.QualityScore, nil
	case "confidence":
		return business.Confidence, nil
	case "reviewed":
		return business.Reviewed, nil
	case "notes":
		return business.Notes, nil
	case "change_status":
		return business.ChangeStatus, nil
	case "tags":
		return business.Tags, nil
	case "maps_url":
		return business.MapsURL, nil
	case "place_id":
		return business.PlaceID, nil
	case "cid":
		return business.CID, nil
	case "data_id":
		return business.DataID, nil
	case "source_job_id":
		return business.SourceJobID, nil
	case "source_query":
		return business.SourceQuery, nil
	case "source_cell":
		return business.SourceCell, nil
	case "scraped_at":
		return exportTimeValue(business.ScrapedAt), nil
	case "updated_at":
		return exportTimeValue(business.UpdatedAt), nil
	case "raw_json":
		if row.Detail == nil {
			return nil, fmt.Errorf("raw data was requested without business detail")
		}
		if json.Valid([]byte(row.Detail.RawJSON)) {
			return json.RawMessage(row.Detail.RawJSON), nil
		}
		return row.Detail.RawJSON, nil
	case "sources_json":
		return marshalExportDetail(row.Detail, func(detail *BusinessDetail) any { return detail.Sources })
	case "provenance_json":
		return marshalExportDetail(row.Detail, func(detail *BusinessDetail) any { return detail.Provenance })
	case "changes_json":
		return marshalExportDetail(row.Detail, func(detail *BusinessDetail) any { return detail.Changes })
	case "prospect_status":
		return exportProspectValue(row).Status, nil
	case "prospect_score":
		return exportProspectValue(row).Score, nil
	case "prospect_tier":
		return exportProspectValue(row).Tier, nil
	case "prospect_reasons":
		reasons := exportProspectValue(row).Reasons
		if reasons == nil {
			reasons = []prospect.Reason{}
		}
		encoded, err := json.Marshal(reasons)
		if err != nil {
			return nil, err
		}
		return json.RawMessage(encoded), nil
	case "call_opener":
		return exportProspectValue(row).Opener, nil
	case "description":
		return business.Description, nil
	case "street":
		return business.Street, nil
	case "plus_code":
		return business.PlusCode, nil
	case "input_id":
		return business.InputID, nil
	case "phone_type":
		return business.PhoneType, nil
	case "emails":
		return strings.Join(business.Emails, "; "), nil
	case "email_type":
		return business.EmailType, nil
	case "email_status":
		return business.EmailStatus, nil
	case "facebook", "instagram", "linkedin", "x", "youtube", "tiktok", "whatsapp":
		return business.Social.URL(key), nil
	case "reviews_per_rating":
		return business.ReviewsPerRating, nil
	case "user_reviews":
		return business.UserReviews, nil
	case "popular_times":
		return business.PopularTimes, nil
	case "technologies":
		return strings.Join(business.Technologies, "; "), nil
	case "last_checked_at":
		if business.LastCheckedAt == nil {
			return nil, nil
		}
		return exportTimeValue(*business.LastCheckedAt), nil
	case "first_seen_at":
		return exportTimeValue(business.FirstSeenAt), nil
	case "last_seen_at":
		return exportTimeValue(business.LastSeenAt), nil
	default:
		return nil, fmt.Errorf("unsupported export column %q", key)
	}
}

func pointerValue[T any](value *T) any {
	if value == nil {
		return nil
	}
	return *value
}

func exportTimeValue(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339)
}

func marshalExportDetail(detail *BusinessDetail, selectValue func(*BusinessDetail) any) (any, error) {
	if detail == nil {
		return nil, fmt.Errorf("optional detail was requested without business detail")
	}
	data, err := json.Marshal(selectValue(detail))
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}

func exportValueString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(typed, 10)
	case int:
		return strconv.Itoa(typed)
	case json.RawMessage:
		return string(typed)
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(data)
	}
}

// discoveredCompaniesExportWriter emits the Lead-Engine ingestion format:
// one DiscoveredCompany JSON object per line. Column selections do not apply;
// the contract fixes the shape.
type discoveredCompaniesExportWriter struct {
	destination string
	temporary   string
	file        *os.File
	buffer      *bufio.Writer
	closed      bool
}

func newDiscoveredCompaniesExportWriter(destination string) (*discoveredCompaniesExportWriter, error) {
	temporary := destination + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create export file: %w", err)
	}
	return &discoveredCompaniesExportWriter{
		destination: destination, temporary: temporary,
		file: file, buffer: bufio.NewWriterSize(file, 128*1024),
	}, nil
}

func (writer *discoveredCompaniesExportWriter) Add(row exportDataRow) error {
	if writer.closed {
		return fmt.Errorf("export writer is closed")
	}
	company := discoveredCompanyFromBusiness(row.Business, exportProspectValue(row))
	encoded, err := json.Marshal(company)
	if err != nil {
		return fmt.Errorf("encode discovered company: %w", err)
	}
	if _, err := writer.buffer.Write(encoded); err != nil {
		return err
	}
	return writer.buffer.WriteByte('\n')
}

func (writer *discoveredCompaniesExportWriter) Close() error {
	if writer.closed {
		return nil
	}
	writer.closed = true
	writeErr := writer.buffer.Flush()
	if syncErr := writer.file.Sync(); writeErr == nil {
		writeErr = syncErr
	}
	if closeErr := writer.file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(writer.temporary)
		return fmt.Errorf("write export file: %w", writeErr)
	}
	if err := os.Rename(writer.temporary, writer.destination); err != nil {
		_ = os.Remove(writer.temporary)
		return fmt.Errorf("publish export file: %w", err)
	}
	return nil
}

func (writer *discoveredCompaniesExportWriter) Abort() {
	if writer == nil {
		return
	}
	if !writer.closed {
		writer.closed = true
		_ = writer.file.Close()
	}
	_ = os.Remove(writer.temporary)
}

// verifyDiscoveredCompaniesExport checks that every line of the generated
// file is one JSON object carrying the contract's providerCompanyId key.
func verifyDiscoveredCompaniesExport(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var company struct {
			ProviderCompanyID *string `json:"providerCompanyId"`
		}
		if err := json.Unmarshal(raw, &company); err != nil {
			return fmt.Errorf("line %d is not valid JSON: %w", line, err)
		}
		if company.ProviderCompanyID == nil || strings.TrimSpace(*company.ProviderCompanyID) == "" {
			return fmt.Errorf("line %d is missing providerCompanyId", line)
		}
	}
	return scanner.Err()
}
