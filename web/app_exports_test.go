package web

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExportWritersProduceValidPortableFormats(t *testing.T) {
	t.Parallel()

	latitude, longitude := 37.7749, -122.4194
	rating := 4.8
	reviews := int64(125)
	business := BusinessResult{
		ID: "business-one", Name: "O'Brien Dental & Smile", PrimaryCategory: "Dentist",
		Address: "1 Market St, San Francisco, CA", City: "San Francisco", State: "CA",
		Latitude: &latitude, Longitude: &longitude, Phone: "+1 415 555 0100",
		PrimaryEmail: "hello@example.test", Website: "https://example.test", Domain: "example.test",
		Rating: &rating, ReviewCount: &reviews, PlaceID: "place-one", SourceJobID: "job-one",
		SourceQuery: "dentists in San Francisco", ScrapedAt: time.Unix(1_800_000_000, 0).UTC(),
	}

	formats := []string{"csv", "json", "jsonl", "geojson", "kml", "vcard", "txt", "postgresql_sql", "mysql_sql"}
	for _, format := range formats {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			if err := writeExportStart(&output, format); err != nil {
				t.Fatalf("writeExportStart() error = %v", err)
			}
			if err := writeExportRow(&output, format, business, true); err != nil {
				t.Fatalf("writeExportRow() error = %v", err)
			}
			if err := writeExportEnd(&output, format); err != nil {
				t.Fatalf("writeExportEnd() error = %v", err)
			}
			contents := output.Bytes()
			if len(contents) == 0 || strings.Contains(string(contents), "<nil>") {
				t.Fatalf("empty or invalid %s output: %q", format, contents)
			}
			switch format {
			case "csv":
				records, err := csv.NewReader(bytes.NewReader(contents)).ReadAll()
				if err != nil || len(records) != 2 || records[1][1] != business.Name {
					t.Fatalf("CSV records = %#v, %v", records, err)
				}
			case "json", "geojson":
				if !json.Valid(contents) {
					t.Fatalf("invalid JSON: %s", contents)
				}
			case "jsonl":
				var decoded BusinessResult
				if err := json.NewDecoder(bytes.NewReader(contents)).Decode(&decoded); err != nil || decoded.ID != business.ID {
					t.Fatalf("JSONL decoded = %+v, %v", decoded, err)
				}
			case "kml":
				decoder := xml.NewDecoder(bytes.NewReader(contents))
				for {
					_, err := decoder.Token()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatalf("invalid KML: %v\n%s", err, contents)
					}
				}
			case "vcard":
				if !strings.Contains(string(contents), "BEGIN:VCARD") || !strings.Contains(string(contents), "END:VCARD") {
					t.Fatalf("invalid VCard: %s", contents)
				}
			case "postgresql_sql", "mysql_sql":
				if strings.Contains(string(contents), "O'Brien") || !strings.Contains(string(contents), "O''Brien") ||
					!strings.HasSuffix(string(contents), "COMMIT;\n") {
					t.Fatalf("invalid SQL escaping/transaction: %s", contents)
				}
			}
		})
	}
}

func TestSafeDataPathRejectsTraversalAndAbsolutePaths(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	valid, err := safeDataPath(base, "exports/results.csv")
	if err != nil || valid != filepath.Join(base, "exports", "results.csv") {
		t.Fatalf("safeDataPath(valid) = %q, %v", valid, err)
	}
	for _, candidate := range []string{"../jobs.db", "exports/../../jobs.db", filepath.Join(base, "outside.csv"), ".", ".."} {
		if path, err := safeDataPath(base, candidate); err == nil {
			t.Fatalf("safeDataPath(%q) unexpectedly accepted %q", candidate, path)
		}
	}
}

func TestExportHelpersBoundNamesFormatsAndEmptyJSON(t *testing.T) {
	t.Parallel()

	if slug := exportSlug("  San Francisco / Dentists !!! "); slug != "san-francisco-dentists" {
		t.Fatalf("exportSlug() = %q", slug)
	}
	for _, format := range []string{"xlsx", "sqlite"} {
		if _, ok := exportExtension(format); !ok {
			t.Fatalf("implemented %s format was not advertised", format)
		}
	}
	for _, format := range []string{"json", "geojson"} {
		var output bytes.Buffer
		if err := writeExportStart(&output, format); err != nil {
			t.Fatal(err)
		}
		if err := writeExportEnd(&output, format); err != nil {
			t.Fatal(err)
		}
		if !json.Valid(output.Bytes()) {
			t.Fatalf("empty %s export is invalid: %s", format, output.String())
		}
	}
}
