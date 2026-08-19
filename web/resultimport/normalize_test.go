package resultimport

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestUnicodeNormalization(t *testing.T) {
	t.Parallel()

	if got, want := NormalizeName("  CAFÉ  Ｄｅｎｔａｌ， L.L.C. "), "café dental"; got != want {
		t.Errorf("NormalizeName = %q, want %q", got, want)
	}
	if got, want := NormalizeName("Clinique Dentaire Québec Ltée"), "clinique dentaire québec ltée"; got != want {
		t.Errorf("Unicode NormalizeName = %q, want %q", got, want)
	}
	if got, want := NormalizeAddress("１２３　Mäin St., Apt #４"), "123 mäin st apt 4"; got != want {
		t.Errorf("NormalizeAddress = %q, want %q", got, want)
	}
	if got, want := NormalizeCategory("  CHILDREN’S   DENTIST "), "children s dentist"; got != want {
		t.Errorf("NormalizeCategory = %q, want %q", got, want)
	}

	website, err := NormalizeURL("https://www.bücher.de/über-uns?utm_source=test")
	if err != nil {
		t.Fatalf("NormalizeURL(IDN): %v", err)
	}
	if website.Host != "www.xn--bcher-kva.de" || website.Domain != "xn--bcher-kva.de" {
		t.Errorf("IDN normalization = %#v", website)
	}
}

func TestURLCanonicalization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		canonical string
		domain    string
	}{
		{"bare", "Example.COM", "https://example.com", "example.com"},
		{"default port", "http://Example.COM:80/a/../b#fragment", "http://example.com/b", "example.com"},
		{"tracking and query order", "https://sub.example.co.uk/?z=3&utm_medium=x&a=1&fbclid=y", "https://sub.example.co.uk?a=1&z=3", "example.co.uk"},
		{"IPv4", "http://127.0.0.1:8080/", "http://127.0.0.1:8080", "127.0.0.1"},
		{"localhost", "localhost:3000/path", "https://localhost:3000/path", "localhost"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			value, err := NormalizeURL(test.input)
			if err != nil {
				t.Fatalf("NormalizeURL: %v", err)
			}
			if value.Canonical != test.canonical || value.Domain != test.domain || !value.Valid {
				t.Errorf("NormalizeURL(%q) = %#v, want canonical=%q domain=%q", test.input, value, test.canonical, test.domain)
			}
		})
	}
	for _, invalid := range []string{"", "n/a", "ftp://example.com", "https://user:password@example.com", "://broken"} {
		if value, err := NormalizeURL(invalid); err == nil || value.Valid {
			t.Errorf("NormalizeURL(%q) = %#v, %v; want invalid", invalid, value, err)
		}
	}
}

func TestPhoneAndEmailNormalizationAndDeduplication(t *testing.T) {
	t.Parallel()

	phones := NormalizePhones("", "(415) 555-2671", "+1 415 555 2671", "415.555.2671 ext 9", "4155552671 x9", "bad")
	if len(phones) != 2 {
		t.Fatalf("phones = %#v, want two unique normalized numbers", phones)
	}
	if phones[0].MatchKey != "4155552671" || phones[0].Extension != "" ||
		phones[1].MatchKey != "4155552671" || phones[1].Extension != "9" {
		t.Errorf("unexpected phones: %#v", phones)
	}
	withCountry := NormalizePhone("020 7946 0958", "44")
	if !withCountry.Valid || withCountry.Normalized != "+4402079460958" {
		t.Errorf("phone with default calling code = %#v", withCountry)
	}

	emails := NormalizeEmails("Info@Example.COM, info@example.com", `["sales@example.com","SALES@example.com","bad"]`)
	if len(emails) != 2 {
		t.Fatalf("emails = %#v, want two unique valid mailboxes", emails)
	}
	if emails[0].Normalized != "info@example.com" || emails[1].Normalized != "sales@example.com" {
		t.Errorf("unexpected emails: %#v", emails)
	}
}

func TestJSONCanonicalizationMakesRecordHashDeterministic(t *testing.T) {
	t.Parallel()

	headers := []string{"place_id", "title", "open_hours", "reviews_per_rating"}
	first := mustReadOne(t, makeCSV(t, headers, []string{
		"place", "Dentist", `{ "Tuesday": ["9-5"], "Monday": ["8-4"] }`, `{"5":10,"4":2}`,
	}), Options{SourceID: "one"})
	second := mustReadOne(t, makeCSV(t, headers, []string{
		"place", "Dentist", `{"Monday":["8-4"],"Tuesday":["9-5"]}`, `{"4":2,"5":10}`,
	}), Options{SourceID: "two"})
	if first.Business.RecordHash != second.Business.RecordHash {
		t.Errorf("semantic record hashes differ: %q != %q", first.Business.RecordHash, second.Business.RecordHash)
	}
	if first.RawHash == second.RawHash {
		t.Error("raw hashes should preserve formatting/key-order differences")
	}
}

func TestExactDeduplicationUtilities(t *testing.T) {
	t.Parallel()

	headers := []string{"place_id", "title", "website", "phone", "address"}
	csvData := makeCSV(t, headers,
		[]string{"place-1", "Alpha", "https://alpha.example", "+1 415 555 1000", "1 Main St, San Francisco, CA 94101"},
		[]string{"place-1", "Alpha Dental", "https://bridge.example", "", "99 Other St, San Francisco, CA 94102"},
		[]string{"place-3", "Bridge Dental", "https://bridge.example/contact", "", "3 Main St, San Francisco, CA 94103"},
		[]string{"place-4", "Separate", "https://separate.example", "", "4 Main St, San Francisco, CA 94104"},
	)
	records, err := ReadAll(context.Background(), strings.NewReader(csvData), Options{})
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	matches := ExactMatchKeys(records[0].Business, records[1].Business)
	if !reflect.DeepEqual(matches, []IdentityKey{{Kind: IdentityPlaceID, Value: "place-1"}}) {
		t.Errorf("exact match keys = %#v", matches)
	}
	if !IsExactDuplicate(records[1].Business, records[2].Business) {
		t.Error("shared normalized domain should be an exact duplicate")
	}
	if IsExactDuplicate(records[0].Business, records[3].Business) {
		t.Error("unrelated records should not be exact duplicates")
	}
	groups := GroupExactDuplicates(records)
	if len(groups) != 1 || !reflect.DeepEqual(groups[0].Records, []int{0, 1, 2}) {
		t.Fatalf("duplicate groups = %#v", groups)
	}
	wantKeys := []IdentityKey{
		{Kind: IdentityPlaceID, Value: "place-1"},
		{Kind: IdentityDomain, Value: "bridge.example"},
	}
	if !reflect.DeepEqual(groups[0].Keys, wantKeys) {
		t.Errorf("duplicate group keys = %#v, want %#v", groups[0].Keys, wantKeys)
	}
}

func TestFallbackIdentityIsStableButSemanticChangesHash(t *testing.T) {
	t.Parallel()

	first := mustReadOne(t, makeCSV(t, []string{"title", "category"}, []string{"Café Dental LLC", "Dentist"}), Options{})
	second := mustReadOne(t, makeCSV(t, []string{"category", "title"}, []string{"Dentist", "CAFÉ DENTAL"}), Options{})
	if first.Business.ID == second.Business.ID {
		t.Error("fallback IDs include raw hash and should not assert an exact identity without a key")
	}
	if first.Business.RecordHash != second.Business.RecordHash {
		t.Error("equivalent normalized business content should have the same record hash")
	}
	if len(first.Business.IdentityKeys) != 0 {
		t.Errorf("unexpected exact keys: %#v", first.Business.IdentityKeys)
	}
	assertWarning(t, first.Warnings, "identity", IssueMissingIdentity)
}

func TestCountryNamesNormalizeToISOAlpha2(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]string{
		"United States": "US", "USA": "US", "us": "US", "United States of America": "US",
		"United Kingdom": "GB", "UK": "GB", "Great Britain": "GB", "gb": "GB",
		"Canada": "CA", "Australia": "AU", "New Zealand": "NZ",
		"Germany": "DE", "Deutschland": "DE",
		"France": "FR", "Spain": "ES", "Italy": "IT",
		"Netherlands": "NL", "The Netherlands": "NL",
		"Ireland": "IE", "India": "IN", "Mexico": "MX",
		"Brazil": "BR", "Brasil": "BR", "Japan": "JP", "China": "CN",
		"Switzerland": "CH", "Austria": "AT", "Belgium": "BE", "Sweden": "SE",
	} {
		if got := normalizeCountry(input); got != want {
			t.Errorf("normalizeCountry(%q) = %q, want %q", input, got, want)
		}
	}

	// Unknown countries must come back unchanged rather than guessed at.
	for input, want := range map[string]string{
		"Nigeria":          "Nigeria",
		"Atlantis":         "Atlantis",
		"  South   Africa": "South Africa",
	} {
		if got := normalizeCountry(input); got != want {
			t.Errorf("normalizeCountry(%q) = %q, want unchanged %q", input, got, want)
		}
	}
}

func TestCanadianProvincesNormalizeLikeUSStates(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]string{
		"Alberta": "AB", "British Columbia": "BC", "Manitoba": "MB",
		"New Brunswick": "NB", "Newfoundland and Labrador": "NL", "Newfoundland": "NL",
		"Northwest Territories": "NT", "Nova Scotia": "NS", "Nunavut": "NU",
		"Ontario": "ON", "Prince Edward Island": "PE",
		"Quebec": "QC", "Québec": "QC", "Saskatchewan": "SK",
		"Yukon": "YT", "Yukon Territory": "YT",
		// Two-letter codes pass through exactly as US state codes do.
		"on": "ON", "qc": "QC", "bc": "BC",
		// US states keep working.
		"California": "CA", "New York": "NY",
	} {
		if got := normalizeState(input); got != want {
			t.Errorf("normalizeState(%q) = %q, want %q", input, got, want)
		}
	}

	if got := normalizeState("Bavaria"); got != "" {
		t.Errorf("normalizeState(Bavaria) = %q, want empty for unknown regions", got)
	}
}

func TestAddressTextCountryDetectionStaysConservative(t *testing.T) {
	t.Parallel()

	canadian := parseAddress("1 Yonge St, Toronto, ON M5E 1E5, Canada", JSONValue{})
	if canadian.Country != "CA" || canadian.City != "Toronto" || canadian.State != "ON" {
		t.Errorf("canadian address = %#v", canadian)
	}

	german := parseAddress("Musterstrasse 1, Berlin, Germany", JSONValue{})
	if german.Country != "DE" || german.City != "Berlin" {
		t.Errorf("german address = %#v", german)
	}

	// The IN state code must never be read as the country India.
	domestic := parseAddress("742 Evergreen Terrace, Indianapolis, IN 46220", JSONValue{})
	if domestic.Country != "" || domestic.State != "IN" || domestic.PostalCode != "46220" {
		t.Errorf("domestic address = %#v", domestic)
	}
}
