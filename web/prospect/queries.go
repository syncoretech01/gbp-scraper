package prospect

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

const (
	// maxZIPCSVRows bounds ParseZIPCSV input.
	maxZIPCSVRows = 100000

	// maxGeneratedQueries bounds BuildQueries output (synonyms x ZIPs).
	maxGeneratedQueries = 5000

	// maxTopZIPs bounds the TopZIPs cap.
	maxTopZIPs = 500
)

// ZIPArea is one ZIP code with enough geography to generate queries and
// centre a map.
type ZIPArea struct {
	ZIP        string
	City       string
	State      string
	Latitude   float64
	Longitude  float64
	Population int
}

// GeneratedQuery is one search query tied to the ZIP area it covers.
type GeneratedQuery struct {
	Query string
	ZIP   ZIPArea
}

// ParseZIPCSV reads ZIP areas from a CSV with the header columns
// zip,city,state,latitude,longitude,population. Column order does not
// matter (columns are located by header name, case-insensitively) and
// extra columns are ignored. Each ZIP must be exactly 5 digits,
// latitude must be within -90..90, longitude within -180..180, and
// population a non-negative integer (blank = 0). Input is bounded to
// 100000 data rows.
func ParseZIPCSV(r io.Reader) ([]ZIPArea, error) {
	reader := csv.NewReader(r)
	reader.TrimLeadingSpace = true

	header, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return nil, errors.New("zip csv is empty")
	}

	if err != nil {
		return nil, fmt.Errorf("read zip csv header: %w", err)
	}

	columns := make(map[string]int, len(header))
	for i, name := range header {
		name = strings.TrimPrefix(name, string(rune(0xFEFF))) // tolerate a UTF-8 BOM
		columns[strings.ToLower(strings.TrimSpace(name))] = i
	}

	for _, required := range []string{"zip", "city", "state", "latitude", "longitude", "population"} {
		if _, ok := columns[required]; !ok {
			return nil, fmt.Errorf("zip csv is missing required column %q", required)
		}
	}

	var areas []ZIPArea

	row := 1 // header was row 1

	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}

		row++

		if err != nil {
			return nil, fmt.Errorf("read zip csv row %d: %w", row, err)
		}

		if len(areas) >= maxZIPCSVRows {
			return nil, fmt.Errorf("zip csv has more than %d data rows; split the file", maxZIPCSVRows)
		}

		field := func(name string) string {
			if i := columns[name]; i < len(record) {
				return strings.TrimSpace(record[i])
			}

			return ""
		}

		zip := field("zip")
		if !isFiveDigitZIP(zip) {
			return nil, fmt.Errorf("zip csv row %d: zip %q must be exactly 5 digits", row, zip)
		}

		lat, err := strconv.ParseFloat(field("latitude"), 64)
		if err != nil {
			return nil, fmt.Errorf("zip csv row %d: invalid latitude %q", row, field("latitude"))
		}

		if lat < -90 || lat > 90 {
			return nil, fmt.Errorf("zip csv row %d: latitude %v out of range -90..90", row, lat)
		}

		lon, err := strconv.ParseFloat(field("longitude"), 64)
		if err != nil {
			return nil, fmt.Errorf("zip csv row %d: invalid longitude %q", row, field("longitude"))
		}

		if lon < -180 || lon > 180 {
			return nil, fmt.Errorf("zip csv row %d: longitude %v out of range -180..180", row, lon)
		}

		population := 0

		if s := field("population"); s != "" {
			population, err = strconv.Atoi(s)
			if err != nil || population < 0 {
				return nil, fmt.Errorf("zip csv row %d: population %q must be a non-negative integer", row, s)
			}
		}

		areas = append(areas, ZIPArea{
			ZIP:        zip,
			City:       field("city"),
			State:      field("state"),
			Latitude:   lat,
			Longitude:  lon,
			Population: population,
		})
	}

	return areas, nil
}

// isFiveDigitZIP reports whether zip is exactly five ASCII digits.
func isFiveDigitZIP(zip string) bool {
	if len(zip) != 5 {
		return false
	}

	for i := 0; i < len(zip); i++ {
		if zip[i] < '0' || zip[i] > '9' {
			return false
		}
	}

	return true
}

// TopZIPs filters areas by state and/or city (case-insensitive; empty
// string means "all"), sorts them by population descending (ties broken
// by ZIP ascending for determinism), and returns at most topN entries.
// topN is bounded to 1..500.
func TopZIPs(areas []ZIPArea, state string, city string, topN int) []ZIPArea {
	if topN < 1 {
		topN = 1
	}

	if topN > maxTopZIPs {
		topN = maxTopZIPs
	}

	state = strings.ToLower(strings.TrimSpace(state))
	city = strings.ToLower(strings.TrimSpace(city))

	filtered := make([]ZIPArea, 0, len(areas))

	for _, area := range areas {
		if state != "" && strings.ToLower(strings.TrimSpace(area.State)) != state {
			continue
		}

		if city != "" && strings.ToLower(strings.TrimSpace(area.City)) != city {
			continue
		}

		filtered = append(filtered, area)
	}

	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Population != filtered[j].Population {
			return filtered[i].Population > filtered[j].Population
		}

		return filtered[i].ZIP < filtered[j].ZIP
	})

	if len(filtered) > topN {
		filtered = filtered[:topN]
	}

	return filtered
}

// BuildQueries produces one search query per (synonym, ZIP) pair in
// deterministic synonym-major order. When includeCityInQuery is true a
// query reads "synonym in City STATE zip", otherwise "synonym zip".
// Synonyms are trimmed, empties dropped, and duplicates removed
// (case-insensitively, first spelling wins); exact-duplicate query
// strings are removed too. It errors when the cleaned synonym count
// times the ZIP count exceeds 5000.
func BuildQueries(synonyms []string, zips []ZIPArea, includeCityInQuery bool) ([]GeneratedQuery, error) {
	cleaned := make([]string, 0, len(synonyms))
	seen := make(map[string]struct{}, len(synonyms))

	for _, synonym := range synonyms {
		synonym = strings.TrimSpace(synonym)
		if synonym == "" {
			continue
		}

		key := strings.ToLower(synonym)
		if _, ok := seen[key]; ok {
			continue
		}

		seen[key] = struct{}{}

		cleaned = append(cleaned, synonym)
	}

	total := len(cleaned) * len(zips)
	if total > maxGeneratedQueries {
		return nil, fmt.Errorf(
			"too many queries: %d synonyms x %d ZIP areas = %d, which exceeds the limit of %d; reduce the synonyms or narrow the ZIP selection",
			len(cleaned), len(zips), total, maxGeneratedQueries,
		)
	}

	queries := make([]GeneratedQuery, 0, total)
	seenQueries := make(map[string]struct{}, total)

	for _, synonym := range cleaned {
		for _, zip := range zips {
			var query string

			if includeCityInQuery {
				query = fmt.Sprintf("%s in %s %s %s", synonym, zip.City, strings.ToUpper(zip.State), zip.ZIP)
			} else {
				query = fmt.Sprintf("%s %s", synonym, zip.ZIP)
			}

			query = strings.Join(strings.Fields(query), " ")

			if _, ok := seenQueries[query]; ok {
				continue
			}

			seenQueries[query] = struct{}{}

			queries = append(queries, GeneratedQuery{Query: query, ZIP: zip})
		}
	}

	return queries, nil
}

// QueryLines flattens generated queries to their query strings, in the
// same order.
func QueryLines(queries []GeneratedQuery) []string {
	lines := make([]string, len(queries))
	for i, q := range queries {
		lines[i] = q.Query
	}

	return lines
}

// Centre returns the population-weighted centroid of the given ZIP
// areas, for use as the job's map centre. When no area has a positive
// population it falls back to the plain average of the coordinates. An
// empty slice yields (0, 0).
func Centre(zips []ZIPArea) (lat, lon float64) {
	if len(zips) == 0 {
		return 0, 0
	}

	var totalPopulation float64

	for _, zip := range zips {
		if zip.Population > 0 {
			totalPopulation += float64(zip.Population)
		}
	}

	if totalPopulation > 0 {
		for _, zip := range zips {
			if zip.Population <= 0 {
				continue
			}

			weight := float64(zip.Population) / totalPopulation
			lat += zip.Latitude * weight
			lon += zip.Longitude * weight
		}

		return lat, lon
	}

	for _, zip := range zips {
		lat += zip.Latitude
		lon += zip.Longitude
	}

	n := float64(len(zips))

	return lat / n, lon / n
}
