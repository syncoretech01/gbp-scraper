package web

import (
	"archive/zip"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	_ "modernc.org/sqlite"
)

func TestConfiguredExportWritersProduceVerifiedFiles(t *testing.T) {
	t.Parallel()

	latitude, longitude := 37.7749, -122.4194
	rating := 4.7
	reviews := int64(82)
	row := exportDataRow{Business: BusinessResult{
		ID: "business-one", Name: "Harbor Dental", PrimaryCategory: "Dentist",
		City: "San Francisco", Latitude: &latitude, Longitude: &longitude,
		Rating: &rating, ReviewCount: &reviews, ScrapedAt: time.Unix(1_800_000_000, 0).UTC(),
	}}
	columns := []ExportColumnSelection{
		{Key: "name", Label: "Business Name"},
		{Key: "city", Label: "Market"},
		{Key: "latitude", Label: "Latitude"},
		{Key: "rating", Label: "Rating"},
	}

	for _, format := range []string{"csv", "json", "geojson", "xlsx", "sqlite", "parquet"} {
		format := format
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			extension, ok := exportExtension(format)
			if !ok {
				t.Fatalf("format %q is not advertised", format)
			}
			path := filepath.Join(t.TempDir(), "result."+extension)
			writer, err := newExportPartWriter(path, format, columns, ExportBuildOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err := writer.Add(row); err != nil {
				writer.Abort()
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := verifyExportFile(format, path); err != nil {
				t.Fatalf("verify %s: %v", format, err)
			}

			switch format {
			case "csv":
				file, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				records, err := csv.NewReader(file).ReadAll()
				_ = file.Close()
				if err != nil || len(records) != 2 || records[0][0] != "Business Name" || records[1][0] != "Harbor Dental" {
					t.Fatalf("CSV records = %#v, %v", records, err)
				}
			case "json", "geojson":
				contents, err := os.ReadFile(path)
				if err != nil || !json.Valid(contents) || !strings.Contains(string(contents), "Business Name") {
					t.Fatalf("invalid %s: %s (%v)", format, contents, err)
				}
			case "sqlite":
				database, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				defer database.Close()
				var name, city string
				if err := database.QueryRow(`SELECT "Business Name", "Market" FROM businesses`).Scan(&name, &city); err != nil {
					t.Fatal(err)
				}
				if name != "Harbor Dental" || city != "San Francisco" {
					t.Fatalf("portable SQLite row = %q, %q", name, city)
				}
			case "parquet":
				// Parquet must round-trip through an independent reader with
				// its column types intact, otherwise the file is only nominally
				// columnar.
				rows, readErr := parquet.ReadFile[any](path)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if len(rows) != 1 {
					t.Fatalf("Parquet rows = %d, want 1", len(rows))
				}
				record, ok := rows[0].(map[string]any)
				if !ok {
					t.Fatalf("Parquet row = %#v", rows[0])
				}
				if record["Business Name"] != "Harbor Dental" || record["Market"] != "San Francisco" {
					t.Fatalf("Parquet row = %#v", record)
				}
				if latitudeValue, valid := record["Latitude"].(float64); !valid || latitudeValue != latitude {
					t.Fatalf("Parquet latitude = %#v, want the typed double %v", record["Latitude"], latitude)
				}
				if ratingValue, valid := record["Rating"].(float64); !valid || ratingValue != rating {
					t.Fatalf("Parquet rating = %#v, want the typed double %v", record["Rating"], rating)
				}
			}
		})
	}
}

func TestExportColumnSelectionSplittingAndZIPAreBounded(t *testing.T) {
	t.Parallel()

	values := exportTestForm{
		"split_by":          {"max_rows"},
		"max_rows_per_file": {"2"},
		"zip":               {"true"},
		"include_raw":       {"true"},
		"deduplicate":       {"true"},
	}
	options, err := parseExportBuildOptions(values)
	if err != nil {
		t.Fatal(err)
	}
	columns, legacy, err := parseExportColumnSpec("city=Market\nname=Business Name", options)
	if err != nil {
		t.Fatal(err)
	}
	if legacy || len(columns) != 3 || columns[0].Key != "city" || columns[1].Label != "Business Name" || columns[2].Key != "raw_json" {
		t.Fatalf("columns = %#v, legacy = %v", columns, legacy)
	}
	for index, expected := range []string{"rows-0001", "rows-0001", "rows-0002"} {
		_, label := exportGroupFor(BusinessResult{}, options, int64(index))
		if label != expected {
			t.Fatalf("row %d group = %q, want %q", index, label, expected)
		}
	}

	directory := t.TempDir()
	first := filepath.Join(directory, "one.txt")
	second := filepath.Join(directory, "two.txt")
	if err := os.WriteFile(first, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(directory, "results.zip")
	if err := writeExportZIP(archivePath, []string{first, second}); err != nil {
		t.Fatal(err)
	}
	if err := verifyZIP(archivePath, 2); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if archive.File[0].Name != "one.txt" || archive.File[1].Name != "two.txt" {
		t.Fatalf("ZIP members = %q, %q", archive.File[0].Name, archive.File[1].Name)
	}
}

func TestJSONExportCreationRequestSupportsStructuredColumnsAndOptions(t *testing.T) {
	t.Parallel()

	body := `{
		"name":"Automation delivery",
		"format":"xlsx",
		"source_scope":"selected",
		"selected_ids":["business-one","business-two"],
		"columns":[{"key":"name","label":"Business Name"},{"key":"city","label":"Market"}],
		"split_by":"max_rows",
		"max_rows_per_file":1000,
		"zip":true,
		"include_provenance":true,
		"deduplicate":true
	}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/exports", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if err := prepareExportCreationForm(httptest.NewRecorder(), request); err != nil {
		t.Fatal(err)
	}
	creation, err := (&Server{}).resolveExportCreation(request)
	if err != nil {
		t.Fatal(err)
	}
	if creation.Format != "xlsx" || creation.Search.Filters[0].Operator != "in" ||
		creation.Search.Filters[0].Value != "business-one,business-two" ||
		len(creation.Columns) != 3 || creation.Columns[0].Label != "Business Name" ||
		!creation.Options.ZIP || creation.Options.MaxRowsPerFile != 1000 || !creation.Options.IncludeProvenance {
		t.Fatalf("JSON export creation = %+v", creation)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/exports", strings.NewReader(`{"name":"x","unknown":true}`))
	request.Header.Set("Content-Type", "application/json")
	if err := prepareExportCreationForm(httptest.NewRecorder(), request); err == nil {
		t.Fatal("JSON export request accepted an unknown field")
	}
}

type exportTestForm map[string][]string

func (values exportTestForm) FormValue(name string) string {
	entries := values[name]
	if len(entries) == 0 {
		return ""
	}
	return entries[0]
}
