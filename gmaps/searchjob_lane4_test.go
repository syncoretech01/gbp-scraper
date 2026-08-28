package gmaps_test

import (
	"encoding/csv"
	"encoding/json"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/gosom/google-maps-scraper/gmaps"
)

// cameraAltitude pulls the "!1d<metres>" camera field out of a built pb value.
func cameraAltitude(t *testing.T, job *gmaps.SearchJob) float64 {
	t.Helper()

	pb, ok := job.URLParams["pb"]
	if !ok {
		t.Fatalf("search job carries no pb parameter: %v", job.URLParams)
	}

	match := regexp.MustCompile(`!1d([0-9.]+)`).FindStringSubmatch(pb)
	if len(match) != 2 {
		t.Fatalf("no !1d camera field in pb: %s", pb)
	}

	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		t.Fatalf("camera field %q is not a number: %v", match[1], err)
	}

	return value
}

func searchJobFor(radius float64) *gmaps.SearchJob {
	return gmaps.NewSearchJob(&gmaps.MapSearchParams{
		Query: "tattoo shop", Hl: "en",
		Location: gmaps.MapLocation{Lat: 34.0522, Lon: -118.2437, ZoomLvl: 12, Radius: radius},
	})
}

// TestSearchJobCameraFollowsRadius pins the Fast-mode request-construction fix.
//
// The camera altitude ("!1d") is what decides how far from the centre Maps
// ranks results; it used to be frozen at ~3826.9 m, so a 15 km Fast run
// retrieved the same few kilometres of city a 2 km run did and the radius only
// trimmed the answer afterwards. Measured live from 34.0522,-118.2437, the
// furthest returned listing moved from 3.4 km at the frozen altitude to
// 12.8 km at 8 x 15 km, so the altitude has to scale with the radius.
func TestSearchJobCameraFollowsRadius(t *testing.T) {
	t.Parallel()

	small := cameraAltitude(t, searchJobFor(2000))
	large := cameraAltitude(t, searchJobFor(15000))

	if large <= small {
		t.Fatalf("a larger radius must widen the search camera: 2 km -> %v, 15 km -> %v", small, large)
	}

	if want := 15000.0 * 8; math.Abs(large-want) > 1 {
		t.Fatalf("15 km radius camera = %v, want %v", large, want)
	}

	// A run that asks for no radius keeps the historical camera exactly, so
	// no existing caller changes behaviour.
	if legacy := cameraAltitude(t, searchJobFor(0)); math.Abs(legacy-3826.902183192154) > 1e-6 {
		t.Fatalf("no-radius camera = %v, want the historical 3826.902183192154", legacy)
	}

	// The bound stops an absurd radius from asking for a planet-wide camera.
	if huge := cameraAltitude(t, searchJobFor(10_000_000)); huge > 400000 {
		t.Fatalf("camera altitude %v exceeds the 400 km bound", huge)
	}
}

// TestSearchJobCameraNoLongerFrozen is the direct regression: the shipped
// build emitted the same camera for every radius.
func TestSearchJobCameraNoLongerFrozen(t *testing.T) {
	t.Parallel()

	seen := map[float64]bool{}
	for _, radius := range []float64{1000, 5000, 15000, 50000} {
		seen[cameraAltitude(t, searchJobFor(radius))] = true
	}

	if len(seen) != 4 {
		t.Fatalf("four different radii produced %d distinct cameras; the radius is being ignored", len(seen))
	}
}

// searchPayload wraps one business record in the envelope ParseSearchResults
// expects.
func searchPayload(t *testing.T, business []any) []byte {
	t.Helper()

	item := make([]any, 15)
	item[14] = business

	raw, err := json.Marshal([]any{[]any{nil, []any{nil, item}}})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	return raw
}

// liveSearchBusiness is the shape Maps' tbm=map search really returns today:
// index 4 holds the rating at [7] and, unless withReviewCount, no count at [8]
// at all; index 78 holds the place id, index 10 the hex feature id, index 13
// the categories.
func liveSearchBusiness(withReviewCount bool) []any {
	business := make([]any, 260)
	business[0] = "S5ORapSPCbCvkdUPi8GLgAg"
	business[2] = []any{"339 1st St", "Los Angeles, CA 90012", "United States"}
	business[9] = []any{nil, nil, 34.04977, -118.23976}
	business[10] = "0x80c2c7c1fb8c44bf:0xda15391219882888"
	business[11] = "Space City Vintage"
	business[13] = []any{"Tattoo shop", "Body piercing shop"}
	business[78] = "ChIJv0SM-8HHwoARiCiIGRI5Fdo"

	rating := []any{nil, nil, nil, nil, nil, nil, nil, 4.5}
	if withReviewCount {
		rating = append(rating, 48.0)
	}

	business[4] = rating

	return business
}

// TestSearchResultsNeverFabricateAZeroReviewCount is the K regression.
//
// Live evidence: the Fast acceptance run wrote review_count "0" for 26 of 26
// rows while every one of them carried a real 4.4-5.0 star rating, because the
// search response no longer contains a count and a missing int is zero.
func TestSearchResultsNeverFabricateAZeroReviewCount(t *testing.T) {
	t.Parallel()

	entries, err := gmaps.ParseSearchResults(searchPayload(t, liveSearchBusiness(false)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("parsed %d entries, want 1", len(entries))
	}

	entry := entries[0]

	if !entry.ReviewCountUnknown {
		t.Fatalf("a payload with no review count must mark the count unknown, got %d", entry.ReviewCount)
	}

	if entry.ReviewRatingUnknown || entry.ReviewRating != 4.5 {
		t.Fatalf("the rating IS in the payload and must survive: %v (unknown=%v)",
			entry.ReviewRating, entry.ReviewRatingUnknown)
	}

	// The CSV cell must be empty, not "0": the normalized importer reads an
	// empty numeric column as NULL and a "0" as a real zero.
	if cell := csvCell(t, entry, "review_count"); cell != "" {
		t.Fatalf("review_count CSV cell = %q, want an empty cell for a value never captured", cell)
	}

	if rating := csvCell(t, entry, "review_rating"); rating != "4.500000" {
		t.Fatalf("review_rating CSV cell = %q, want the captured rating", rating)
	}
}

// TestSearchResultsKeepACapturedZero proves the fix does not erase a genuine
// zero: a listing that really reports 0 reviews still exports 0.
func TestSearchResultsKeepACapturedZero(t *testing.T) {
	t.Parallel()

	business := make([]any, 260)
	business[4] = []any{nil, nil, nil, nil, nil, nil, nil, 4.5, 0.0}
	business[11] = "Zero Reviews Inc"
	business[9] = []any{nil, nil, 1.0, 2.0}

	entries, err := gmaps.ParseSearchResults(searchPayload(t, business))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if entries[0].ReviewCountUnknown {
		t.Fatal("a payload that really carries 0 must not be marked unknown")
	}

	if cell := csvCell(t, entries[0], "review_count"); cell != "0" {
		t.Fatalf("captured zero exported as %q, want 0", cell)
	}
}

// TestSearchResultsCaptureStrongestIdentity is the second half of K: the Fast
// acceptance run produced 5 businesses with no place_id, no cid and no
// maps_url, yet the payload it parsed carried a ChIJ place id at index 78 and
// a hex feature id at index 10 for every one of them.
func TestSearchResultsCaptureStrongestIdentity(t *testing.T) {
	t.Parallel()

	entries, err := gmaps.ParseSearchResults(searchPayload(t, liveSearchBusiness(false)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	entry := entries[0]

	if entry.PlaceID != "ChIJv0SM-8HHwoARiCiIGRI5Fdo" {
		t.Fatalf("place_id = %q, want the id carried at index 78", entry.PlaceID)
	}

	// 0xda15391219882888 in decimal.
	if entry.Cid != "15714529224679762056" {
		t.Fatalf("cid = %q, want the decimal form of the feature id", entry.Cid)
	}

	if !strings.Contains(entry.Link, entry.PlaceID) {
		t.Fatalf("link = %q, want a maps link built from the place id", entry.Link)
	}

	if entry.Category != "Tattoo shop" {
		t.Fatalf("category = %q, want the first parsed category", entry.Category)
	}
}

// TestCidIsNeverFabricated: an id that is not the hex pair Maps uses must
// produce no cid rather than a made-up one.
func TestCidIsNeverFabricated(t *testing.T) {
	t.Parallel()

	for _, dataID := range []string{"", "not-an-id", "0xzz:0xzz", "0x80c2c7c1fb8c44bf"} {
		business := make([]any, 260)
		business[10] = dataID
		business[11] = "X"
		business[9] = []any{nil, nil, 1.0, 2.0}

		entries, err := gmaps.ParseSearchResults(searchPayload(t, business))
		if err != nil {
			t.Fatalf("parse %q: %v", dataID, err)
		}

		if entries[0].Cid != "" {
			t.Fatalf("data_id %q produced cid %q; it must produce none", dataID, entries[0].Cid)
		}

		if entries[0].Link != "" {
			t.Fatalf("data_id %q produced link %q with no identity", dataID, entries[0].Link)
		}
	}
}

// TestFrozenFixtureStillParses guards the change against the archived search
// response, which DOES carry a review count at [4][8].
func TestFrozenFixtureStillParses(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../testdata/output.json")
	if err != nil {
		t.Skipf("fixture unavailable: %v", err)
	}

	entries, err := gmaps.ParseSearchResults(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if len(entries) == 0 {
		t.Fatal("fixture parsed to no entries")
	}

	for _, entry := range entries {
		if entry.ReviewCountUnknown {
			t.Fatalf("%q: the fixture carries a review count and must keep it", entry.Title)
		}

		if entry.ReviewCount <= 0 {
			t.Fatalf("%q: review count = %d", entry.Title, entry.ReviewCount)
		}
	}
}

// csvCell reads one named column out of an entry's CSV row.
func csvCell(t *testing.T, entry *gmaps.Entry, column string) string {
	t.Helper()

	buffer := &strings.Builder{}
	writer := csv.NewWriter(buffer)

	if err := writer.Write(entry.CsvHeaders()); err != nil {
		t.Fatalf("write headers: %v", err)
	}

	if err := writer.Write(entry.CsvRow()); err != nil {
		t.Fatalf("write row: %v", err)
	}

	writer.Flush()

	records, err := csv.NewReader(strings.NewReader(buffer.String())).ReadAll()
	if err != nil {
		t.Fatalf("read back csv: %v", err)
	}

	for i, name := range records[0] {
		if name == column {
			return records[1][i]
		}
	}

	t.Fatalf("column %q not in header %v", column, records[0])

	return ""
}
