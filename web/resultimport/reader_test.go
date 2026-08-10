package resultimport

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLegacyHeadersAndRealRow(t *testing.T) {
	t.Parallel()

	headers := LegacyHeaders()
	if len(headers) != LegacyColumnCount {
		t.Fatalf("legacy header count = %d, want %d", len(headers), LegacyColumnCount)
	}
	wantHeaders := []string{
		"input_id", "link", "title", "category", "address", "open_hours", "popular_times", "website",
		"phone", "plus_code", "review_count", "review_rating", "reviews_per_rating", "latitude", "longitude",
		"cid", "status", "descriptions", "reviews_link", "thumbnail", "timezone", "price_range", "data_id",
		"street_view_url", "place_id", "images", "reservations", "order_online", "menu", "owner",
		"complete_address", "credit_cards_accepted", "about", "user_reviews", "user_reviews_extended", "emails",
	}
	if !reflect.DeepEqual(headers, wantHeaders) {
		t.Fatalf("legacy headers do not match scraper contract:\n got: %v\nwant: %v", headers, wantHeaders)
	}

	row := []string{
		"source-17",
		"https://www.google.com/maps?cid=123&utm_source=import",
		"  Acme Dental, LLC  ",
		"Dentist",
		"123 Main St, San Francisco, CA 94102, United States",
		`{"Monday":["09:00-17:00"]}`,
		`{"Monday":{"9":20}}`,
		"HTTP://WWW.Example.COM:80/contact/../?utm_source=maps&b=2&a=1#top",
		"+1 (415) 555-2671",
		"QH8V+9Q San Francisco",
		"1,234",
		"4.7",
		`{"5":1000,"4":150}`,
		"37.7749",
		"-122.4194",
		"123456789",
		"OPERATIONAL",
		"Neighborhood dental practice",
		"https://example.com/reviews",
		"https://example.com/thumb.jpg",
		"America/Los_Angeles",
		"$$",
		"0x8085808c:0x1234ABCD",
		"https://maps.example/street-view",
		"ChIJ123AbC",
		`[{"title":"front","image":"https://example.com/front.jpg"}]`,
		`[{"link":"https://book.example.com","source":"Book"}]`,
		`[]`,
		`{"link":"https://example.com/menu","source":"Menu"}`,
		`{"id":"owner-1","name":"Owner","link":"https://example.com"}`,
		`{"borough":"","street":"123 Main St","city":"San Francisco","postal_code":"94102","state":"California","country":"United States"}`,
		"Visa, visa, MasterCard",
		`[{"id":"accessibility","name":"Accessibility","options":[]}]`,
		`[]`,
		`[]`,
		"INFO@EXAMPLE.COM, info@example.com, invalid-mailbox",
	}

	records, err := ReadAll(context.Background(), strings.NewReader(makeCSV(t, headers, row)), Options{
		SourceID:           "job-17-results",
		JobID:              "job-17",
		Query:              "dentists in San Francisco",
		GridCell:           "cell-4-2",
		ObservedAt:         time.Date(2026, time.August, 6, 10, 30, 0, 0, time.FixedZone("PDT", -7*60*60)),
		DefaultCallingCode: "1",
	})
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("record count = %d, want 1", len(records))
	}
	record := records[0]
	if record.Business.Name != "Acme Dental, LLC" || record.Business.NormalizedName != "acme dental" {
		t.Fatalf("unexpected names: display=%q normalized=%q", record.Business.Name, record.Business.NormalizedName)
	}
	if record.Business.Website.Canonical != "http://www.example.com?a=1&b=2" {
		t.Errorf("canonical website = %q", record.Business.Website.Canonical)
	}
	if record.Business.Website.Domain != "example.com" {
		t.Errorf("domain = %q, want example.com", record.Business.Website.Domain)
	}
	if len(record.Business.Phones) != 1 || record.Business.Phones[0].MatchKey != "4155552671" {
		t.Errorf("phones = %#v", record.Business.Phones)
	}
	if len(record.Business.Emails) != 1 || record.Business.Emails[0].Normalized != "info@example.com" {
		t.Errorf("emails = %#v", record.Business.Emails)
	}
	if record.Business.ReviewCount == nil || *record.Business.ReviewCount != 1234 {
		t.Errorf("review count = %v", record.Business.ReviewCount)
	}
	if record.Business.ReviewRating == nil || *record.Business.ReviewRating != 4.7 {
		t.Errorf("review rating = %v", record.Business.ReviewRating)
	}
	if record.Business.Latitude == nil || record.Business.Longitude == nil {
		t.Fatal("expected parsed coordinates")
	}
	if record.Business.Address.City != "San Francisco" || record.Business.Address.State != "CA" ||
		record.Business.Address.PostalCode != "94102" || record.Business.Address.Country != "US" {
		t.Errorf("structured address = %#v", record.Business.Address)
	}
	if !record.Business.Structured.OpenHours.Valid || !record.Business.Structured.CompleteAddress.Valid {
		t.Errorf("structured JSON not parsed: %#v", record.Business.Structured)
	}
	if len(record.Business.CreditCardsAccepted) != 2 {
		t.Errorf("credit cards = %#v, want two unique values", record.Business.CreditCardsAccepted)
	}
	if record.Source.InputID != "source-17" || record.Source.Query != "dentists in San Francisco" || record.Source.GridCell != "cell-4-2" {
		t.Errorf("source provenance = %#v", record.Source)
	}
	wantObserved := time.Date(2026, time.August, 6, 17, 30, 0, 0, time.UTC)
	if record.Source.ObservedAt == nil || !record.Source.ObservedAt.Equal(wantObserved) {
		t.Errorf("observed at = %v, want %v", record.Source.ObservedAt, wantObserved)
	}
	if record.Business.ID == "" || record.Business.RecordHash == "" || record.RawHash == "" || record.Cursor.Token == "" {
		t.Errorf("stable identifiers missing: %#v", record)
	}
	if record.Raw.Value("title") != row[2] || record.Raw.Value("emails") != row[35] {
		t.Error("raw cell values were not preserved")
	}
	assertWarning(t, record.Warnings, "emails", IssueInvalidEmail)
	assertWarning(t, record.Warnings, "emails", IssueDuplicateContact)
}

func TestReorderedAndMissingColumnsHaveDeterministicHashes(t *testing.T) {
	t.Parallel()

	firstHeaders := []string{"place_id", "title", "website", "phone", "emails"}
	firstRow := []string{"PlaceCaseSensitive", "Café Dental LLC", "https://EXAMPLE.com/?utm_campaign=x", "(415) 555-0100", "A@Example.com"}
	secondHeaders := []string{"emails", "phone", "web_site", "business_name", "place_id"}
	secondRow := []string{"A@Example.com", "(415) 555-0100", "https://EXAMPLE.com/?utm_campaign=x", "Café Dental LLC", "PlaceCaseSensitive"}

	first := mustReadOne(t, makeCSV(t, firstHeaders, firstRow), Options{SourceID: "same-source"})
	second := mustReadOne(t, makeCSV(t, secondHeaders, secondRow), Options{SourceID: "same-source"})
	if first.Business.ID != second.Business.ID {
		t.Errorf("business IDs differ: %q != %q", first.Business.ID, second.Business.ID)
	}
	if first.Business.RecordHash != second.Business.RecordHash {
		t.Errorf("record hashes differ: %q != %q", first.Business.RecordHash, second.Business.RecordHash)
	}
	if first.RawHash != second.RawHash {
		t.Errorf("raw hashes differ across header reorder: %q != %q", first.RawHash, second.RawHash)
	}
	if first.Cursor.Token != second.Cursor.Token {
		t.Errorf("cursor differs across header reorder: %q != %q", first.Cursor.Token, second.Cursor.Token)
	}
	if len(first.Raw.Headers) != 5 || first.Raw.Value("address") != "" {
		t.Errorf("subset header handling failed: %#v", first.Raw)
	}
}

func TestRowMetadataOverridesDefaults(t *testing.T) {
	t.Parallel()

	headers := []string{"title", "query", "grid_cell", "scraped_at", "source_link"}
	row := []string{"Dentist", "row query", "8:12", "2026-08-05T22:00:58Z", "https://example.com/source?utm_source=x"}
	record := mustReadOne(t, makeCSV(t, headers, row), Options{
		SourceID:   "source",
		Query:      "default query",
		GridCell:   "default cell",
		SourceURL:  "https://default.example",
		ObservedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if record.Source.Query != "row query" || record.Source.GridCell != "8:12" {
		t.Errorf("row metadata did not override defaults: %#v", record.Source)
	}
	if record.Source.SourceURL != "https://example.com/source" {
		t.Errorf("source URL = %q", record.Source.SourceURL)
	}
	if record.Source.ObservedAt == nil || record.Source.ObservedAt.Format(time.RFC3339) != "2026-08-05T22:00:58Z" {
		t.Errorf("source timestamp = %v", record.Source.ObservedAt)
	}
}

func TestMalformedValuesAreRetainedAndWarned(t *testing.T) {
	t.Parallel()

	headers := []string{
		"title", "website", "phone", "emails", "review_count", "review_rating", "latitude", "longitude",
		"open_hours", "source_timestamp",
	}
	row := []string{
		"Broken Row", "ftp://user:supersecret@example.com", "12", "supersecret-not-email", "many", "NaN", "91", "-181",
		`{"Monday":`, "not-a-date",
	}
	record := mustReadOne(t, makeCSV(t, headers, row), Options{SourceID: "malformed"})
	if record.Business.Website.Valid || len(record.Business.Phones) != 0 || len(record.Business.Emails) != 0 {
		t.Errorf("malformed contact values accepted: website=%#v phones=%#v emails=%#v",
			record.Business.Website, record.Business.Phones, record.Business.Emails)
	}
	if record.Business.ReviewCount != nil || record.Business.ReviewRating != nil ||
		record.Business.Latitude != nil || record.Business.Longitude != nil {
		t.Error("malformed or out-of-range numeric values should be nil")
	}
	if record.Business.Structured.OpenHours.Valid {
		t.Error("malformed JSON should not be valid")
	}
	if record.Source.ObservedAt != nil {
		t.Error("malformed source timestamp should be nil")
	}
	for _, want := range []struct {
		field string
		code  IssueCode
	}{
		{"website", IssueUnsupportedScheme},
		{"phone", IssueInvalidPhone},
		{"emails", IssueInvalidEmail},
		{"review_count", IssueInvalidInteger},
		{"review_rating", IssueInvalidNumber},
		{"latitude", IssueOutOfRange},
		{"longitude", IssueOutOfRange},
		{"open_hours", IssueInvalidJSON},
		{"source_timestamp", IssueInvalidTimestamp},
	} {
		assertWarning(t, record.Warnings, want.field, want.code)
	}
	if record.Raw.Value("website") != row[1] || record.Raw.Value("emails") != row[3] {
		t.Error("malformed raw values were not preserved")
	}
}

func TestResumeCursorAndRepeatedRows(t *testing.T) {
	t.Parallel()

	csvData := makeCSV(t, []string{"place_id", "title"},
		[]string{"one", "First"},
		[]string{"two", "Second"},
		[]string{"two", "Second"},
		[]string{"three", "Third"},
	)
	all, err := ReadAll(context.Background(), strings.NewReader(csvData), Options{SourceID: "cursor-source"})
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("record count = %d, want 4", len(all))
	}
	if all[1].Cursor.Token == all[2].Cursor.Token || all[1].Cursor.Occurrence != 1 || all[2].Cursor.Occurrence != 2 {
		t.Fatalf("repeated row cursors are not unique/idempotent: %#v %#v", all[1].Cursor, all[2].Cursor)
	}
	resumed, err := ReadAll(context.Background(), strings.NewReader(csvData), Options{
		SourceID:    "cursor-source",
		AfterCursor: all[1].Cursor.Token,
	})
	if err != nil {
		t.Fatalf("resume ReadAll: %v", err)
	}
	if len(resumed) != 2 || resumed[0].Cursor.Token != all[2].Cursor.Token || resumed[1].Business.PlaceID != "three" {
		t.Errorf("resumed rows = %#v", resumed)
	}
	if _, err := ParseCursor(all[1].Cursor.Token); err != nil {
		t.Errorf("ParseCursor(valid): %v", err)
	}
	if _, err := ReadAll(context.Background(), strings.NewReader(csvData), Options{
		SourceID: "cursor-source", AfterCursor: cursorPrefix + strings.Repeat("0", 64),
	}); !errors.Is(err, ErrCursorNotFound) {
		t.Errorf("missing cursor error = %v, want ErrCursorNotFound", err)
	}
	if _, err := ReadAll(context.Background(), strings.NewReader(csvData), Options{
		SourceID: "cursor-source", AfterCursor: "contains-a-secret",
	}); !errors.Is(err, ErrInvalidCursor) {
		t.Errorf("invalid cursor error = %v, want ErrInvalidCursor", err)
	}
}

func TestCancellationBeforeAndDuringStream(t *testing.T) {
	t.Parallel()

	csvData := makeCSV(t, []string{"place_id", "title"},
		[]string{"one", "First"},
		[]string{"two", "Second"},
	)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadAll(canceled, strings.NewReader(csvData), Options{}); !errors.Is(err, context.Canceled) {
		t.Errorf("pre-canceled ReadAll error = %v", err)
	}

	ctx, stop := context.WithCancel(context.Background())
	count := 0
	err := Stream(ctx, strings.NewReader(csvData), Options{}, func(Record) error {
		count++
		stop()

		return nil
	})
	if !errors.Is(err, context.Canceled) || count != 1 {
		t.Errorf("Stream cancellation: err=%v count=%d", err, count)
	}
}

func TestStructuralErrorsDoNotLeakValues(t *testing.T) {
	t.Parallel()

	secret := "supersecret-token-8192"
	malformed := "title,website\nDentist,\"https://" + secret + ".example\n"
	_, err := ReadAll(context.Background(), strings.NewReader(malformed), Options{})
	if !errors.Is(err, ErrMalformedCSV) {
		t.Fatalf("malformed CSV error = %v, want ErrMalformedCSV", err)
	}
	if strings.Contains(fmt.Sprint(err), secret) {
		t.Fatalf("parse error leaked source value: %v", err)
	}

	record := mustReadOne(t, makeCSV(t, []string{"title", "website", "emails"}, []string{
		"Dentist", "https://user:" + secret + "@example.com", secret + "@",
	}), Options{})
	if !strings.Contains(record.Raw.Value("website"), secret) {
		t.Fatal("raw preservation fixture is invalid")
	}
	for _, warning := range record.Warnings {
		if strings.Contains(warning.String(), secret) || strings.Contains(warning.Message, secret) {
			t.Fatalf("warning leaked source value: %v", warning)
		}
	}
	if strings.Contains(record.String(), secret) || strings.Contains(record.Business.String(), secret) ||
		strings.Contains(record.Cursor.String(), secret) {
		t.Fatal("log-safe String method leaked source value")
	}
}

func TestHeaderAndRowShapeHandling(t *testing.T) {
	t.Parallel()

	if _, err := ReadAll(context.Background(), strings.NewReader(""), Options{}); !errors.Is(err, ErrEmptyCSV) {
		t.Errorf("empty CSV error = %v", err)
	}
	if _, err := ReadAll(context.Background(), strings.NewReader("website,web_site\na,b\n"), Options{}); !errors.Is(err, ErrDuplicateHeader) {
		t.Errorf("duplicate canonical header error = %v", err)
	}
	short := mustReadOne(t, "title,phone,email\nDentist\n", Options{})
	if short.Raw.Value("phone") != "" || len(short.Raw.Values) != 1 {
		t.Errorf("short row not preserved/mapped: %#v", short.Raw)
	}
	extra := mustReadOne(t, "title\nDentist,hidden-extra\n", Options{})
	if extra.Raw.Value("_extra_1") != "hidden-extra" {
		t.Errorf("extra column not preserved: %#v", extra.Raw)
	}
	assertWarning(t, extra.Warnings, "_row", IssueExtraColumns)

	reader := NewReader(strings.NewReader(" Name , Longitude \nDentist,-122\n"), Options{})
	header, err := reader.Header(context.Background())
	if err != nil {
		t.Fatalf("Header: %v", err)
	}
	if !reflect.DeepEqual(header, []string{"title", "longitude"}) {
		t.Errorf("canonical header = %#v", header)
	}
	if _, err := reader.Next(context.Background()); err != nil {
		t.Errorf("Next after Header: %v", err)
	}
	if _, err := reader.Next(context.Background()); !errors.Is(err, io.EOF) {
		t.Errorf("terminal Next error = %v, want io.EOF", err)
	}
}

func TestImportLimits(t *testing.T) {
	t.Parallel()

	_, err := ReadAll(context.Background(), strings.NewReader("title\nvery-long-value\n"), Options{MaxFieldBytes: 4})
	if !errors.Is(err, ErrRecordTooLarge) {
		t.Errorf("field limit error = %v", err)
	}
	_, err = ReadAll(context.Background(), strings.NewReader("one,two,three\na,b,c\n"), Options{MaxColumns: 2})
	if !errors.Is(err, ErrInvalidHeader) {
		t.Errorf("header column limit error = %v", err)
	}
}

func makeCSV(t *testing.T, headers []string, rows ...[]string) string {
	t.Helper()

	var output strings.Builder
	writer := csv.NewWriter(&output)
	if err := writer.Write(headers); err != nil {
		t.Fatalf("write headers: %v", err)
	}
	for _, row := range rows {
		if err := writer.Write(row); err != nil {
			t.Fatalf("write row: %v", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		t.Fatalf("flush CSV: %v", err)
	}

	return output.String()
}

func mustReadOne(t *testing.T, csvData string, options Options) Record {
	t.Helper()

	records, err := ReadAll(context.Background(), strings.NewReader(csvData), options)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("record count = %d, want 1", len(records))
	}

	return records[0]
}

func assertWarning(t *testing.T, warnings []Warning, field string, code IssueCode) {
	t.Helper()

	for _, warning := range warnings {
		if warning.Field == field && warning.Code == code {
			return
		}
	}
	t.Errorf("missing warning field=%q code=%q in %#v", field, code, warnings)
}
