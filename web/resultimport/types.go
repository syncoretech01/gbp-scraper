// Package resultimport reads legacy scraper CSV output and turns each row into
// normalized business and source records. It deliberately has no database
// dependency so callers can use it for migrations, previews, and exports.
package resultimport

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	// LegacyColumnCount is the number of columns emitted by gmaps.Entry.CsvRow.
	LegacyColumnCount = 36

	defaultMaxColumns    = 256
	defaultMaxFieldBytes = 16 << 20
	defaultMaxRowBytes   = 64 << 20
)

var (
	// ErrEmptyCSV is returned when a stream has no header record.
	ErrEmptyCSV = errors.New("CSV stream is empty")
	// ErrInvalidHeader is returned for an empty or otherwise unusable header.
	ErrInvalidHeader = errors.New("CSV header is invalid")
	// ErrDuplicateHeader is returned when two headers resolve to the same field.
	ErrDuplicateHeader = errors.New("CSV header contains duplicate fields")
	// ErrMalformedCSV is returned when encoding/csv cannot parse a record.
	ErrMalformedCSV = errors.New("CSV record is malformed")
	// ErrReadCSV is returned when the source reader fails for a non-CSV reason.
	ErrReadCSV = errors.New("CSV stream could not be read")
	// ErrRecordTooLarge is returned when configured import limits are exceeded.
	ErrRecordTooLarge = errors.New("CSV record exceeds import limits")
	// ErrCursorNotFound indicates that a requested resume cursor is not present.
	ErrCursorNotFound = errors.New("resume cursor was not found")
	// ErrInvalidCursor is returned for malformed or unsupported cursor tokens.
	ErrInvalidCursor = errors.New("row cursor is invalid")
	// ErrInvalidURL is returned when a URL cannot be safely canonicalized.
	ErrInvalidURL = errors.New("URL is invalid")
)

// LegacyHeaders returns a fresh copy of the 36-column legacy CSV header.
func LegacyHeaders() []string {
	return []string{
		"input_id",
		"link",
		"title",
		"category",
		"address",
		"open_hours",
		"popular_times",
		"website",
		"phone",
		"plus_code",
		"review_count",
		"review_rating",
		"reviews_per_rating",
		"latitude",
		"longitude",
		"cid",
		"status",
		"descriptions",
		"reviews_link",
		"thumbnail",
		"timezone",
		"price_range",
		"data_id",
		"street_view_url",
		"place_id",
		"images",
		"reservations",
		"order_online",
		"menu",
		"owner",
		"complete_address",
		"credit_cards_accepted",
		"about",
		"user_reviews",
		"user_reviews_extended",
		"emails",
	}
}

// IssueCode identifies a non-fatal normalization problem without embedding the
// offending value in errors or logs.
type IssueCode string

const (
	IssueExtraColumns      IssueCode = "extra_columns"
	IssueInvalidURL        IssueCode = "invalid_url"
	IssueURLCredentials    IssueCode = "url_credentials"
	IssueInvalidPhone      IssueCode = "invalid_phone"
	IssueInvalidEmail      IssueCode = "invalid_email"
	IssueInvalidInteger    IssueCode = "invalid_integer"
	IssueInvalidNumber     IssueCode = "invalid_number"
	IssueOutOfRange        IssueCode = "out_of_range"
	IssueInvalidJSON       IssueCode = "invalid_json"
	IssueInvalidTimestamp  IssueCode = "invalid_timestamp"
	IssueSuspiciousValue   IssueCode = "suspicious_value"
	IssueDuplicateContact  IssueCode = "duplicate_contact"
	IssueUnsupportedScheme IssueCode = "unsupported_url_scheme"
	IssueMissingIdentity   IssueCode = "missing_identity"
	// IssueDomainMismatch marks a contact whose domain does not belong to the
	// business's own website. It is a review prompt, never a rejection: a
	// mailbox provider or a marketing domain is a perfectly normal reason.
	IssueDomainMismatch IssueCode = "domain_mismatch"
)

// Warning describes a non-fatal row issue. Message is intentionally generic;
// the original value remains available only through RawRecord.
type Warning struct {
	Field   string    `json:"field"`
	Code    IssueCode `json:"code"`
	Message string    `json:"message"`
}

// String returns a log-safe warning representation.
func (w Warning) String() string {
	return fmt.Sprintf("field %q: %s", w.Field, w.Code)
}

// RawRecord preserves the exact decoded CSV values and their original header
// spelling. Fields is keyed by canonical header name; OriginalFields retains
// unknown and aliased names exactly as supplied (apart from a leading BOM).
type RawRecord struct {
	Headers        []string          `json:"headers"`
	Values         []string          `json:"values"`
	Fields         map[string]string `json:"fields"`
	OriginalFields map[string]string `json:"original_fields"`
}

// Value returns the raw value for a canonical field name.
func (r RawRecord) Value(field string) string {
	if value, ok := r.Fields[field]; ok {
		return value
	}

	return r.Fields[canonicalHeader(field)]
}

// URLValue contains both a source URL and its safe canonical forms.
type URLValue struct {
	Raw       string `json:"raw"`
	Canonical string `json:"canonical"`
	Host      string `json:"host"`
	Domain    string `json:"domain"`
	Valid     bool   `json:"valid"`
}

// Phone contains a display value and deterministic comparison forms.
type Phone struct {
	Raw        string `json:"raw"`
	Normalized string `json:"normalized"`
	MatchKey   string `json:"match_key"`
	Extension  string `json:"extension,omitempty"`
	Valid      bool   `json:"valid"`
}

// Email contains a display value and its normalized mailbox and domain.
type Email struct {
	Raw        string `json:"raw"`
	Normalized string `json:"normalized"`
	Domain     string `json:"domain"`
	Valid      bool   `json:"valid"`
}

// Address contains raw, normalized, and best-effort structured address data.
type Address struct {
	Raw        string `json:"raw"`
	Normalized string `json:"normalized"`
	Borough    string `json:"borough,omitempty"`
	Street     string `json:"street,omitempty"`
	City       string `json:"city,omitempty"`
	State      string `json:"state,omitempty"`
	PostalCode string `json:"postal_code,omitempty"`
	Country    string `json:"country,omitempty"`
}

// JSONValue is a parsed legacy JSON cell. Empty cells are not Present; invalid
// cells are Present but not Valid and remain available in RawRecord.
type JSONValue struct {
	Present bool            `json:"present"`
	Valid   bool            `json:"valid"`
	Value   json.RawMessage `json:"value,omitempty"`
}

// StructuredFields contains the JSON-valued legacy scraper columns.
type StructuredFields struct {
	OpenHours           JSONValue `json:"open_hours"`
	PopularTimes        JSONValue `json:"popular_times"`
	ReviewsPerRating    JSONValue `json:"reviews_per_rating"`
	Images              JSONValue `json:"images"`
	Reservations        JSONValue `json:"reservations"`
	OrderOnline         JSONValue `json:"order_online"`
	Menu                JSONValue `json:"menu"`
	Owner               JSONValue `json:"owner"`
	CompleteAddress     JSONValue `json:"complete_address"`
	About               JSONValue `json:"about"`
	UserReviews         JSONValue `json:"user_reviews"`
	UserReviewsExtended JSONValue `json:"user_reviews_extended"`
}

// IdentityKind names an exact deduplication key.
type IdentityKind string

const (
	IdentityPlaceID IdentityKind = "place_id"
	IdentityCID     IdentityKind = "cid"
	IdentityDataID  IdentityKind = "data_id"
	IdentityPhone   IdentityKind = "phone"
	IdentityDomain  IdentityKind = "domain"
	IdentityAddress IdentityKind = "address"
)

// IdentityKey is a normalized exact-match key.
type IdentityKey struct {
	Kind  IdentityKind `json:"kind"`
	Value string       `json:"value"`
}

// String returns an unambiguous qualified identity key.
func (k IdentityKey) String() string {
	return string(k.Kind) + ":" + k.Value
}

// Business is the typed, normalized representation of one legacy CSV row.
type Business struct {
	ID                   string           `json:"id"`
	CanonicalIdentityKey string           `json:"canonical_identity_key"`
	IdentityKeys         []IdentityKey    `json:"identity_keys"`
	Name                 string           `json:"name"`
	NormalizedName       string           `json:"normalized_name"`
	Category             string           `json:"category"`
	NormalizedCategory   string           `json:"normalized_category"`
	Address              Address          `json:"address"`
	MapsURL              URLValue         `json:"maps_url"`
	Website              URLValue         `json:"website"`
	Phones               []Phone          `json:"phones"`
	Emails               []Email          `json:"emails"`
	PlaceID              string           `json:"place_id"`
	CID                  string           `json:"cid"`
	DataID               string           `json:"data_id"`
	PlusCode             string           `json:"plus_code"`
	Status               string           `json:"status"`
	Description          string           `json:"description"`
	ReviewsURL           URLValue         `json:"reviews_url"`
	ThumbnailURL         URLValue         `json:"thumbnail_url"`
	StreetViewURL        URLValue         `json:"street_view_url"`
	Timezone             string           `json:"timezone"`
	PriceRange           string           `json:"price_range"`
	ReviewCount          *int64           `json:"review_count,omitempty"`
	ReviewRating         *float64         `json:"review_rating,omitempty"`
	Latitude             *float64         `json:"latitude,omitempty"`
	Longitude            *float64         `json:"longitude,omitempty"`
	CreditCardsAccepted  []string         `json:"credit_cards_accepted"`
	Structured           StructuredFields `json:"structured"`
	RecordHash           string           `json:"record_hash"`
}

// String returns a log-safe business summary that excludes raw contact data.
func (b Business) String() string {
	return fmt.Sprintf("business id=%s keys=%d", b.ID, len(b.IdentityKeys))
}

// Source records where and when a business observation originated.
type Source struct {
	SourceID   string     `json:"source_id"`
	JobID      string     `json:"job_id,omitempty"`
	InputID    string     `json:"input_id,omitempty"`
	Query      string     `json:"query,omitempty"`
	GridCell   string     `json:"grid_cell,omitempty"`
	SourceURL  string     `json:"source_url,omitempty"`
	ObservedAt *time.Time `json:"observed_at,omitempty"`
	RowNumber  int64      `json:"row_number"`
}

// Cursor is an opaque, idempotent marker for a decoded source row. Token is a
// digest and therefore never contains source values or credentials.
type Cursor struct {
	Token      string `json:"token"`
	RowNumber  int64  `json:"row_number"`
	RowHash    string `json:"row_hash"`
	Occurrence int    `json:"occurrence"`
}

// String returns the opaque cursor token.
func (c Cursor) String() string {
	return c.Token
}

// Record combines normalized business data, source provenance, raw values,
// diagnostics, and an idempotent row cursor.
type Record struct {
	Business Business  `json:"business"`
	Source   Source    `json:"source"`
	Raw      RawRecord `json:"raw"`
	Cursor   Cursor    `json:"cursor"`
	RawHash  string    `json:"raw_hash"`
	Warnings []Warning `json:"warnings,omitempty"`
}

// String returns a log-safe record summary.
func (r Record) String() string {
	return fmt.Sprintf("normalized record row=%d id=%s warnings=%d", r.Source.RowNumber, r.Business.ID, len(r.Warnings))
}

// Options supplies provenance, resume behavior, and defensive CSV limits.
// Row-level query, grid-cell, timestamp, and URL columns override these values.
type Options struct {
	SourceID           string
	JobID              string
	Query              string
	GridCell           string
	SourceURL          string
	ObservedAt         time.Time
	DefaultCallingCode string
	AfterCursor        string
	Comma              rune
	Comment            rune
	LazyQuotes         bool
	TrimLeadingSpace   bool
	MaxColumns         int
	MaxFieldBytes      int
	MaxRowBytes        int
}

func (o Options) withDefaults() Options {
	if o.Comma == 0 {
		o.Comma = ','
	}
	if o.MaxColumns <= 0 {
		o.MaxColumns = defaultMaxColumns
	}
	if o.MaxFieldBytes <= 0 {
		o.MaxFieldBytes = defaultMaxFieldBytes
	}
	if o.MaxRowBytes <= 0 {
		o.MaxRowBytes = defaultMaxRowBytes
	}

	return o
}

// DuplicateGroup contains records connected by one or more exact identity
// keys. Records are indexes into the slice supplied to GroupExactDuplicates.
type DuplicateGroup struct {
	Records []int         `json:"records"`
	Keys    []IdentityKey `json:"keys"`
}
