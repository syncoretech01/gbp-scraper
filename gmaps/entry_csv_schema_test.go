package gmaps_test

import (
	"reflect"
	"testing"

	"github.com/gosom/google-maps-scraper/gmaps"
)

// TestEntryCsvHeadersAreFrozenSchema pins the per-job CSV header and its exact
// column order. The per-job UUID CSV is a compatibility surface: retry/restart
// merges rows by this fixed layout and the normalized importer maps each column
// by position, so reordering, renaming, inserting, or dropping a column
// silently corrupts existing result files and every downstream import. This
// test freezes the contract; changing it is a breaking change that must be made
// deliberately, not as a side effect of a refactor.
func TestEntryCsvHeadersAreFrozenSchema(t *testing.T) {
	t.Parallel()

	want := []string{
		"input_id", "link", "title", "category", "address", "open_hours", "popular_times", "website",
		"phone", "plus_code", "review_count", "review_rating", "reviews_per_rating", "latitude", "longitude",
		"cid", "status", "descriptions", "reviews_link", "thumbnail", "timezone", "price_range", "data_id",
		"street_view_url", "place_id", "images", "reservations", "order_online", "menu", "owner",
		"complete_address", "credit_cards_accepted", "about", "user_reviews", "user_reviews_extended", "emails",
	}

	got := (&gmaps.Entry{}).CsvHeaders()

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("per-job CSV header schema changed:\n got: %v\nwant: %v", got, want)
	}
}

// TestEntryCsvRowMatchesHeaderWidth guards the row builder against the header:
// a row that is wider or narrower than the header writes a malformed CSV that
// the importer cannot align by position.
func TestEntryCsvRowMatchesHeaderWidth(t *testing.T) {
	t.Parallel()

	entry := &gmaps.Entry{}

	headers := entry.CsvHeaders()
	row := entry.CsvRow()

	if len(row) != len(headers) {
		t.Fatalf("CsvRow width = %d, CsvHeaders width = %d; they must stay equal", len(row), len(headers))
	}
}
