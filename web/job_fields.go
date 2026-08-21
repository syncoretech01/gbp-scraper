package web

import (
	"fmt"
	"strings"
)

// Maps field groups offered by the New Scrape wizard's data-field step. The
// three groups mirror the specification's Core details / Identifiers /
// Extended details split.
const (
	JobFieldGroupCore        = "core"
	JobFieldGroupIdentifiers = "identifiers"
	JobFieldGroupExtended    = "extended"
)

// Storage classes describe, honestly, where a captured Maps field ends up in
// this workspace. Nothing here changes what the engine collects: the scraper
// always writes its complete CSV row, so a deselected field is still gathered
// and still present in the per-job CSV. The class explains what the workspace
// can then do with it.
const (
	// JobFieldStorageColumn is a normalized SQLite column that also has a
	// dedicated export-builder column, so it can be displayed and exported
	// on its own.
	JobFieldStorageColumn = "column"
	// JobFieldStorageRecord is a normalized SQLite column reachable through
	// the record drawer, the detail API, and the raw_json export column, but
	// with no dedicated export column of its own.
	JobFieldStorageRecord = "record"
	// JobFieldStorageRaw is kept verbatim inside businesses.raw_json only.
	JobFieldStorageRaw = "raw"
)

// MaximumJobFields bounds a stored field selection so one job definition can
// never grow without limit.
const MaximumJobFields = 64

// JobField is one selectable Maps field in the wizard's data-field step.
type JobField struct {
	// Key is the stable identifier stored in JobData.Fields.
	Key string `json:"key"`
	// Label is the operator-facing name.
	Label string `json:"label"`
	// Group is one of the JobFieldGroup* constants.
	Group string `json:"group"`
	// Storage is one of the JobFieldStorage* constants.
	Storage string `json:"storage"`
	// ExportColumn is the export-builder column key this field maps onto, or
	// an empty string when the field has no dedicated export column.
	ExportColumn string `json:"export_column,omitempty"`
	// CSVColumns are the per-job CSV columns the engine always writes for
	// this field. They are informational: the CSV schema is fixed.
	CSVColumns []string `json:"csv_columns,omitempty"`
	// Required marks a field the workspace cannot operate without.
	Required bool `json:"required,omitempty"`
	// Note is the honest one-line explanation shown next to the control.
	Note string `json:"note,omitempty"`
}

// jobFieldCatalogue is the complete, ordered list of Maps fields the engine
// captures today. Every entry was checked against gmaps.Entry.CsvHeaders and
// against the normalized businesses table, so the wizard never offers a field
// the scraper does not actually collect.
var jobFieldCatalogue = []JobField{
	{
		Key: "name", Label: "Name", Group: JobFieldGroupCore,
		Storage: JobFieldStorageColumn, ExportColumn: "name", CSVColumns: []string{"title"},
		Required: true, Note: "Every stored record needs a name, so this cannot be deselected.",
	},
	{
		Key: "category", Label: "Category", Group: JobFieldGroupCore,
		Storage: JobFieldStorageColumn, ExportColumn: "category", CSVColumns: []string{"category"},
	},
	{
		Key: "additional_categories", Label: "Additional categories", Group: JobFieldGroupCore,
		Storage: JobFieldStorageColumn, ExportColumn: "additional_categories",
		Note: "Parsed from the listing's category list where Maps exposes more than one.",
	},
	{
		Key: "address", Label: "Address", Group: JobFieldGroupCore,
		Storage: JobFieldStorageColumn, ExportColumn: "address",
		CSVColumns: []string{"address", "complete_address"},
	},
	{
		Key: "phone", Label: "Phone", Group: JobFieldGroupCore,
		Storage: JobFieldStorageColumn, ExportColumn: "phone", CSVColumns: []string{"phone"},
	},
	{
		Key: "website", Label: "Website", Group: JobFieldGroupCore,
		Storage: JobFieldStorageColumn, ExportColumn: "website", CSVColumns: []string{"website"},
	},
	{
		Key: "domain", Label: "Domain", Group: JobFieldGroupCore,
		Storage: JobFieldStorageColumn, ExportColumn: "domain",
		Note: "Derived locally from the website URL.",
	},
	{
		Key: "coordinates", Label: "Coordinates", Group: JobFieldGroupCore,
		Storage: JobFieldStorageColumn, ExportColumn: "latitude",
		CSVColumns: []string{"latitude", "longitude"},
		Note:       "Latitude and longitude travel together; the map explorer needs both.",
	},
	{
		Key: "rating", Label: "Rating", Group: JobFieldGroupCore,
		Storage: JobFieldStorageColumn, ExportColumn: "rating", CSVColumns: []string{"review_rating"},
	},
	{
		Key: "reviews", Label: "Review count", Group: JobFieldGroupCore,
		Storage: JobFieldStorageColumn, ExportColumn: "review_count", CSVColumns: []string{"review_count"},
	},
	{
		Key: "business_status", Label: "Business status", Group: JobFieldGroupCore,
		Storage: JobFieldStorageColumn, ExportColumn: "business_status", CSVColumns: []string{"status"},
	},

	{
		Key: "place_id", Label: "Place ID", Group: JobFieldGroupIdentifiers,
		Storage: JobFieldStorageColumn, ExportColumn: "place_id", CSVColumns: []string{"place_id"},
	},
	{
		Key: "cid", Label: "CID", Group: JobFieldGroupIdentifiers,
		Storage: JobFieldStorageColumn, ExportColumn: "cid", CSVColumns: []string{"cid"},
	},
	{
		Key: "data_id", Label: "Data ID", Group: JobFieldGroupIdentifiers,
		Storage: JobFieldStorageColumn, ExportColumn: "data_id", CSVColumns: []string{"data_id"},
	},
	{
		Key: "input_id", Label: "Input ID", Group: JobFieldGroupIdentifiers,
		Storage: JobFieldStorageRecord, CSVColumns: []string{"input_id"},
		Note: "Stored on the normalized record; the export builder has no separate input_id column.",
	},
	{
		Key: "source_query", Label: "Source query", Group: JobFieldGroupIdentifiers,
		Storage: JobFieldStorageColumn, ExportColumn: "source_query",
		Note: "The generated query line this observation came from.",
	},
	{
		Key: "source_cell", Label: "Source grid cell", Group: JobFieldGroupIdentifiers,
		Storage: JobFieldStorageColumn, ExportColumn: "source_cell",
		Note: "The deterministic grid cell the observation came from.",
	},

	{
		Key: "opening_hours", Label: "Opening hours", Group: JobFieldGroupExtended,
		Storage: JobFieldStorageRecord, CSVColumns: []string{"open_hours"},
		Note: "Normalized into businesses.open_hours; exportable through raw_json.",
	},
	{
		Key: "popular_times", Label: "Popular times", Group: JobFieldGroupExtended,
		Storage: JobFieldStorageRecord, CSVColumns: []string{"popular_times"},
		Note: "Normalized into businesses.popular_times; exportable through raw_json.",
	},
	{
		Key: "descriptions", Label: "Descriptions", Group: JobFieldGroupExtended,
		Storage: JobFieldStorageRecord, CSVColumns: []string{"descriptions", "about"},
		Note: "Listing description and the About panel.",
	},
	{
		Key: "price_range", Label: "Price range", Group: JobFieldGroupExtended,
		Storage: JobFieldStorageRecord, CSVColumns: []string{"price_range"},
	},
	{
		Key: "images", Label: "Images", Group: JobFieldGroupExtended,
		Storage: JobFieldStorageRaw, CSVColumns: []string{"images", "thumbnail", "street_view_url"},
		Note: "Image URLs only; no image file is downloaded.",
	},
	{
		Key: "reservations", Label: "Reservations", Group: JobFieldGroupExtended,
		Storage: JobFieldStorageRaw, CSVColumns: []string{"reservations"},
	},
	{
		Key: "ordering_links", Label: "Ordering links", Group: JobFieldGroupExtended,
		Storage: JobFieldStorageRaw, CSVColumns: []string{"order_online"},
	},
	{
		Key: "menus", Label: "Menus", Group: JobFieldGroupExtended,
		Storage: JobFieldStorageRaw, CSVColumns: []string{"menu"},
	},
	{
		Key: "owner", Label: "Owner information", Group: JobFieldGroupExtended,
		Storage: JobFieldStorageRaw, CSVColumns: []string{"owner"},
	},
	{
		Key: "reviews_text", Label: "Reviews", Group: JobFieldGroupExtended,
		Storage: JobFieldStorageRaw, CSVColumns: []string{"user_reviews", "user_reviews_extended", "reviews_link"},
		Note: "Individual review text. Extra review pages need the enrichment step's extra-reviews switch.",
	},
}

// JobFieldCatalogue returns a copy of the complete field catalogue in wizard
// display order.
func JobFieldCatalogue() []JobField {
	catalogue := make([]JobField, len(jobFieldCatalogue))
	copy(catalogue, jobFieldCatalogue)

	return catalogue
}

// JobFieldByKey looks one catalogue entry up by its stable key.
func JobFieldByKey(key string) (JobField, bool) {
	for _, field := range jobFieldCatalogue {
		if field.Key == key {
			return field, true
		}
	}

	return JobField{}, false
}

// DefaultJobFieldKeys is every catalogue key. An empty JobData.Fields means
// exactly this, which is why an older saved job keeps today's behaviour.
func DefaultJobFieldKeys() []string {
	keys := make([]string, 0, len(jobFieldCatalogue))
	for _, field := range jobFieldCatalogue {
		keys = append(keys, field.Key)
	}

	return keys
}

// RequiredJobFieldKeys are the fields the workspace cannot operate without.
func RequiredJobFieldKeys() []string {
	keys := make([]string, 0, 1)
	for _, field := range jobFieldCatalogue {
		if field.Required {
			keys = append(keys, field.Key)
		}
	}

	return keys
}

// NormalizeJobFieldKeys validates a selection, removes duplicates, adds the
// required fields, and returns the result in catalogue order. An empty
// selection stays empty, which the rest of the product reads as "retain
// everything" — the historical behaviour.
func NormalizeJobFieldKeys(selected []string) ([]string, error) {
	if len(selected) == 0 {
		return nil, nil
	}
	if len(selected) > MaximumJobFields {
		return nil, fmt.Errorf("at most %d data fields may be selected", MaximumJobFields)
	}

	chosen := make(map[string]struct{}, len(selected))
	for _, raw := range selected {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		if _, ok := JobFieldByKey(key); !ok {
			return nil, fmt.Errorf("unknown data field %q", key)
		}
		chosen[key] = struct{}{}
	}
	if len(chosen) == 0 {
		return nil, nil
	}
	for _, key := range RequiredJobFieldKeys() {
		chosen[key] = struct{}{}
	}

	normalized := make([]string, 0, len(chosen))
	for _, field := range jobFieldCatalogue {
		if _, ok := chosen[field.Key]; ok {
			normalized = append(normalized, field.Key)
		}
	}
	if len(normalized) == len(jobFieldCatalogue) {
		// A full selection is the default; storing nothing keeps the saved
		// job definition identical to one created before this step existed.
		return nil, nil
	}

	return normalized, nil
}

// SelectedJobFields resolves a stored selection into catalogue entries.
func SelectedJobFields(selected []string) []JobField {
	if len(selected) == 0 {
		return JobFieldCatalogue()
	}

	chosen := make(map[string]struct{}, len(selected))
	for _, key := range selected {
		chosen[key] = struct{}{}
	}

	fields := make([]JobField, 0, len(chosen))
	for _, field := range jobFieldCatalogue {
		if _, ok := chosen[field.Key]; ok {
			fields = append(fields, field)
		}
	}

	return fields
}

// JobFieldExportColumnKeys maps a field selection onto export-builder column
// keys, in the export builder's own column order. Record identity, source
// lineage, and the capture timestamp are always included so an export stays
// joinable back to the workspace.
func JobFieldExportColumnKeys(selected []string) []string {
	always := []string{"id", "source_job_id", "scraped_at"}
	wanted := make(map[string]struct{}, len(always)+len(selected))
	for _, key := range always {
		wanted[key] = struct{}{}
	}
	for _, field := range SelectedJobFields(selected) {
		if field.ExportColumn == "" {
			continue
		}
		wanted[field.ExportColumn] = struct{}{}
		if field.Key == "coordinates" {
			wanted["longitude"] = struct{}{}
		}
		if field.Key == "address" {
			for _, part := range []string{"city", "state", "postal_code", "country"} {
				wanted[part] = struct{}{}
			}
		}
	}
	// Anything a selected field can only reach through raw_json is exported
	// through the raw_json column, which is the honest way to offer it.
	for _, field := range SelectedJobFields(selected) {
		if field.Storage == JobFieldStorageRaw || field.Storage == JobFieldStorageRecord {
			wanted["raw_json"] = struct{}{}

			break
		}
	}

	keys := make([]string, 0, len(wanted))
	for _, definition := range exportColumnDefinitions {
		if _, ok := wanted[definition.Key]; ok {
			keys = append(keys, definition.Key)
		}
	}

	return keys
}

// JobFieldExportSpec renders a field selection as the export builder's
// "key=label" column specification.
func JobFieldExportSpec(selected []string) string {
	keys := JobFieldExportColumnKeys(selected)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key+"="+key)
	}

	return strings.Join(lines, "\n")
}
