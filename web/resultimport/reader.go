package resultimport

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// CSVError identifies a structural import failure without including CSV values.
type CSVError struct {
	Row int64
	Err error
}

// Error returns a log-safe structural error message.
func (e *CSVError) Error() string {
	if e.Row <= 0 {
		return e.Err.Error()
	}

	return fmt.Sprintf("CSV row %d: %s", e.Row, e.Err)
}

// Unwrap supports errors.Is checks against the package sentinel errors.
func (e *CSVError) Unwrap() error {
	return e.Err
}

type headerField struct {
	original  string
	canonical string
}

// Reader incrementally parses and normalizes a legacy CSV stream.
type Reader struct {
	csvReader       *csv.Reader
	options         Options
	headers         []headerField
	canonicalHeader []string
	initialized     bool
	done            bool
	rowNumber       int64
	sourceID        string
	occurrences     map[string]int
	resumePending   bool
}

// NewReader creates a streaming legacy result reader. Parsing begins on the
// first Header or Next call.
func NewReader(input io.Reader, options Options) *Reader {
	options = options.withDefaults()
	decoder := csv.NewReader(input)
	decoder.Comma = options.Comma
	decoder.Comment = options.Comment
	decoder.LazyQuotes = options.LazyQuotes
	decoder.TrimLeadingSpace = options.TrimLeadingSpace
	decoder.FieldsPerRecord = -1
	decoder.ReuseRecord = false

	return &Reader{
		csvReader:     decoder,
		options:       options,
		occurrences:   make(map[string]int),
		resumePending: options.AfterCursor != "",
	}
}

// Header returns a copy of the canonical CSV header.
func (r *Reader) Header(ctx context.Context) ([]string, error) {
	if err := r.initialize(ctx); err != nil {
		return nil, err
	}

	return append([]string(nil), r.canonicalHeader...), nil
}

// Next returns the next normalized row or io.EOF. If AfterCursor is set, rows
// through that cursor are consumed before the first record is returned.
func (r *Reader) Next(ctx context.Context) (Record, error) {
	if err := r.initialize(ctx); err != nil {
		return Record{}, err
	}
	if r.done {
		return Record{}, io.EOF
	}

	for {
		if err := ctx.Err(); err != nil {
			return Record{}, err
		}
		values, err := r.csvReader.Read()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Record{}, ctxErr
		}
		if errors.Is(err, io.EOF) {
			r.done = true
			if r.resumePending {
				return Record{}, ErrCursorNotFound
			}

			return Record{}, io.EOF
		}
		if err != nil {
			return Record{}, classifyReadError(r.rowNumber+1, err)
		}

		r.rowNumber++
		if err := r.validateValues(values); err != nil {
			return Record{}, &CSVError{Row: r.rowNumber, Err: err}
		}
		record := r.normalizeRow(values)
		if r.resumePending {
			if record.Cursor.Token == r.options.AfterCursor {
				r.resumePending = false
			}
			continue
		}

		return record, nil
	}
}

// ReadAll parses an entire CSV stream into memory.
func ReadAll(ctx context.Context, input io.Reader, options Options) ([]Record, error) {
	reader := NewReader(input, options)
	records := make([]Record, 0)
	for {
		record, err := reader.Next(ctx)
		if errors.Is(err, io.EOF) {
			return records, nil
		}
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
}

// Stream parses a CSV and invokes consume once per normalized row. It checks
// context cancellation before reading and before every callback.
func Stream(ctx context.Context, input io.Reader, options Options, consume func(Record) error) error {
	reader := NewReader(input, options)
	for {
		record, err := reader.Next(ctx)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := consume(record); err != nil {
			return err
		}
	}
}

func (r *Reader) initialize(ctx context.Context) error {
	if r.initialized {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.options.AfterCursor != "" {
		if _, err := ParseCursor(r.options.AfterCursor); err != nil {
			return err
		}

	}
	values, err := r.csvReader.Read()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, io.EOF) {
		return ErrEmptyCSV
	}
	if err != nil {
		return classifyReadError(0, err)
	}
	if len(values) == 0 || len(values) > r.options.MaxColumns {
		return ErrInvalidHeader
	}
	if err := validateSize(values, r.options.MaxFieldBytes, r.options.MaxRowBytes); err != nil {
		return &CSVError{Err: err}
	}

	r.headers = make([]headerField, len(values))
	r.canonicalHeader = make([]string, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, original := range values {
		if index == 0 {
			original = strings.TrimPrefix(original, "\ufeff")
		}
		canonical := canonicalHeader(original)
		if canonical == "" {
			return ErrInvalidHeader
		}
		if _, exists := seen[canonical]; exists {
			return ErrDuplicateHeader
		}
		seen[canonical] = struct{}{}
		r.headers[index] = headerField{original: original, canonical: canonical}
		r.canonicalHeader[index] = canonical
	}

	r.sourceID = strings.TrimSpace(r.options.SourceID)
	if r.sourceID == "" {
		r.sourceID = strings.TrimSpace(r.options.JobID)
	}
	if r.sourceID == "" {
		sortedHeaders := append([]string(nil), r.canonicalHeader...)
		sortStrings(sortedHeaders)
		r.sourceID = "csv_" + hashParts("csv-source-v1", sortedHeaders...)[:32]
	}
	r.initialized = true

	return nil
}

func (r *Reader) validateValues(values []string) error {
	if len(values) > r.options.MaxColumns {
		return ErrRecordTooLarge
	}

	return validateSize(values, r.options.MaxFieldBytes, r.options.MaxRowBytes)
}

func validateSize(values []string, maxFieldBytes, maxRowBytes int) error {
	total := 0
	for _, value := range values {
		if len(value) > maxFieldBytes {
			return ErrRecordTooLarge
		}
		total += len(value)
		if total > maxRowBytes {
			return ErrRecordTooLarge
		}
	}

	return nil
}

func classifyReadError(row int64, err error) error {
	var parseError *csv.ParseError
	if errors.As(err, &parseError) {
		return &CSVError{Row: row, Err: ErrMalformedCSV}
	}

	return &CSVError{Row: row, Err: ErrReadCSV}
}

func (r *Reader) normalizeRow(values []string) Record {
	raw := RawRecord{
		Headers:        make([]string, len(r.headers)),
		Values:         append([]string(nil), values...),
		Fields:         make(map[string]string, max(len(values), len(r.headers))),
		OriginalFields: make(map[string]string, max(len(values), len(r.headers))),
	}
	for index, header := range r.headers {
		raw.Headers[index] = header.original
		value := ""
		if index < len(values) {
			value = values[index]
		}
		raw.Fields[header.canonical] = value
		raw.OriginalFields[header.original] = value
	}
	warnings := make([]Warning, 0)
	if len(values) > len(r.headers) {
		for index := len(r.headers); index < len(values); index++ {
			name := fmt.Sprintf("_extra_%d", index-len(r.headers)+1)
			raw.Fields[name] = values[index]
			raw.OriginalFields[name] = values[index]
		}
		warnings = append(warnings, warning("_row", IssueExtraColumns))
	}

	structured := StructuredFields{
		OpenHours:           parseJSONValue(raw.Value("open_hours")),
		PopularTimes:        parseJSONValue(raw.Value("popular_times")),
		ReviewsPerRating:    parseJSONValue(raw.Value("reviews_per_rating")),
		Images:              parseJSONValue(raw.Value("images")),
		Reservations:        parseJSONValue(raw.Value("reservations")),
		OrderOnline:         parseJSONValue(raw.Value("order_online")),
		Menu:                parseJSONValue(raw.Value("menu")),
		Owner:               parseJSONValue(raw.Value("owner")),
		CompleteAddress:     parseJSONValue(raw.Value("complete_address")),
		About:               parseJSONValue(raw.Value("about")),
		UserReviews:         parseJSONValue(raw.Value("user_reviews")),
		UserReviewsExtended: parseJSONValue(raw.Value("user_reviews_extended")),
	}
	for field, value := range map[string]JSONValue{
		"open_hours": structured.OpenHours, "popular_times": structured.PopularTimes,
		"reviews_per_rating": structured.ReviewsPerRating, "images": structured.Images,
		"reservations": structured.Reservations, "order_online": structured.OrderOnline,
		"menu": structured.Menu, "owner": structured.Owner,
		"complete_address": structured.CompleteAddress, "about": structured.About,
		"user_reviews": structured.UserReviews, "user_reviews_extended": structured.UserReviewsExtended,
	} {
		if value.Present && !value.Valid {
			warnings = append(warnings, warning(field, IssueInvalidJSON))
		}
	}

	website := normalizeURLField(raw.Value("website"), "website", &warnings)
	mapsURL := normalizeURLField(raw.Value("link"), "link", &warnings)
	reviewsURL := normalizeURLField(raw.Value("reviews_link"), "reviews_link", &warnings)
	thumbnailURL := normalizeURLField(raw.Value("thumbnail"), "thumbnail", &warnings)
	streetViewURL := normalizeURLField(raw.Value("street_view_url"), "street_view_url", &warnings)
	phones, invalidPhones, duplicatePhones := normalizePhones(r.options.DefaultCallingCode, raw.Value("phone"))
	if invalidPhones > 0 {
		warnings = append(warnings, warning("phone", IssueInvalidPhone))
	}
	if duplicatePhones > 0 {
		warnings = append(warnings, warning("phone", IssueDuplicateContact))
	}
	emails, invalidEmails, duplicateEmails := normalizeEmails(raw.Value("emails"))
	if invalidEmails > 0 {
		warnings = append(warnings, warning("emails", IssueInvalidEmail))
	}
	if duplicateEmails > 0 {
		warnings = append(warnings, warning("emails", IssueDuplicateContact))
	}

	// Placeholder-looking contacts are flagged for review but never dropped:
	// the values stay in the record exactly as normalized above.
	for _, phone := range phones {
		if isSuspiciousPhone(phone.MatchKey) {
			warnings = append(warnings, warning("phone", IssueSuspiciousValue))
			break
		}
	}
	if isSuspiciousWebsite(raw.Value("website"), website) {
		warnings = append(warnings, warning("website", IssueSuspiciousValue))
	}
	for _, email := range emails {
		if isSuspiciousEmail(email) {
			warnings = append(warnings, warning("emails", IssueSuspiciousValue))
			break
		}
	}

	reviewCount := parseInteger(raw.Value("review_count"), "review_count", &warnings)
	if reviewCount != nil && *reviewCount < 0 {
		reviewCount = nil
		warnings = append(warnings, warning("review_count", IssueOutOfRange))
	}
	reviewRating := parseNumber(raw.Value("review_rating"), "review_rating", &warnings)
	if reviewRating != nil && (*reviewRating < 0 || *reviewRating > 5) {
		reviewRating = nil
		warnings = append(warnings, warning("review_rating", IssueOutOfRange))
	}
	latitude := parseNumber(raw.Value("latitude"), "latitude", &warnings)
	if latitude != nil && (*latitude < -90 || *latitude > 90) {
		latitude = nil
		warnings = append(warnings, warning("latitude", IssueOutOfRange))
	}
	longitude := parseNumber(raw.Value("longitude"), "longitude", &warnings)
	if longitude != nil && (*longitude < -180 || *longitude > 180) {
		longitude = nil
		warnings = append(warnings, warning("longitude", IssueOutOfRange))
	}

	business := Business{
		Name:                cleanDisplay(raw.Value("title")),
		NormalizedName:      NormalizeName(raw.Value("title")),
		Category:            cleanDisplay(raw.Value("category")),
		NormalizedCategory:  NormalizeCategory(raw.Value("category")),
		Address:             parseAddress(raw.Value("address"), structured.CompleteAddress),
		MapsURL:             mapsURL,
		Website:             website,
		Phones:              phones,
		Emails:              emails,
		PlaceID:             cleanIdentifier(raw.Value("place_id"), false),
		CID:                 cleanIdentifier(raw.Value("cid"), true),
		DataID:              cleanIdentifier(raw.Value("data_id"), true),
		PlusCode:            cleanDisplay(raw.Value("plus_code")),
		Status:              normalizeWords(raw.Value("status"), false),
		Description:         cleanDisplay(raw.Value("descriptions")),
		ReviewsURL:          reviewsURL,
		ThumbnailURL:        thumbnailURL,
		StreetViewURL:       streetViewURL,
		Timezone:            strings.TrimSpace(raw.Value("timezone")),
		PriceRange:          strings.TrimSpace(raw.Value("price_range")),
		ReviewCount:         reviewCount,
		ReviewRating:        reviewRating,
		Latitude:            latitude,
		Longitude:           longitude,
		CreditCardsAccepted: normalizeStringList(raw.Value("credit_cards_accepted")),
		Structured:          structured,
	}
	rawHash := rawRecordHash(raw)
	finalizeBusiness(&business, rawHash)
	if len(business.IdentityKeys) == 0 {
		warnings = append(warnings, warning("identity", IssueMissingIdentity))
	}

	r.occurrences[rawHash]++
	cursor := cursorFor(r.sourceID, rawHash, r.rowNumber, r.occurrences[rawHash])
	source := r.sourceFor(raw, mapsURL, &warnings)
	source.RowNumber = r.rowNumber

	return Record{
		Business: business,
		Source:   source,
		Raw:      raw,
		Cursor:   cursor,
		RawHash:  rawHash,
		Warnings: warnings,
	}
}

func (r *Reader) sourceFor(raw RawRecord, mapsURL URLValue, warnings *[]Warning) Source {
	query := firstNonEmptyRaw(raw, "source_query")
	if query == "" {
		query = strings.TrimSpace(r.options.Query)
	}
	gridCell := firstNonEmptyRaw(raw, "source_grid_cell")
	if gridCell == "" {
		gridCell = strings.TrimSpace(r.options.GridCell)
	}

	sourceURL := firstNonEmptyRaw(raw, "source_url")
	if sourceURL == "" {
		sourceURL = r.options.SourceURL
	}
	if sourceURL == "" && mapsURL.Valid {
		sourceURL = mapsURL.Canonical
	} else if sourceURL != "" {
		normalized := normalizeURLField(sourceURL, "source_url", warnings)
		if normalized.Valid {
			sourceURL = normalized.Canonical
		} else {
			sourceURL = ""
		}
	}

	var observedAt *time.Time
	timestamp := firstNonEmptyRaw(raw, "source_timestamp")
	if timestamp != "" {
		parsed, ok := parseTimestamp(timestamp)
		if ok {
			observedAt = &parsed
		} else {
			*warnings = append(*warnings, warning("source_timestamp", IssueInvalidTimestamp))
		}
	}
	if observedAt == nil && !r.options.ObservedAt.IsZero() {
		value := r.options.ObservedAt.UTC()
		observedAt = &value
	}

	return Source{
		SourceID:   r.sourceID,
		JobID:      strings.TrimSpace(r.options.JobID),
		InputID:    strings.TrimSpace(raw.Value("input_id")),
		Query:      query,
		GridCell:   gridCell,
		SourceURL:  sourceURL,
		ObservedAt: observedAt,
	}
}

func normalizeURLField(raw, field string, warnings *[]Warning) URLValue {
	if strings.TrimSpace(raw) == "" || isMissingValue(raw) {
		return URLValue{Raw: raw}
	}
	normalized, err := NormalizeURL(raw)
	if err == nil {
		return normalized
	}
	code := IssueInvalidURL
	if errors.Is(err, errURLCredentials) {
		code = IssueURLCredentials
	} else if errors.Is(err, errURLScheme) {
		code = IssueUnsupportedScheme
	}
	*warnings = append(*warnings, warning(field, code))

	return URLValue{Raw: raw}
}

func parseInteger(raw, field string, warnings *[]Warning) *int64 {
	value := strings.TrimSpace(raw)
	if value == "" || isMissingValue(value) {
		return nil
	}
	value = strings.ReplaceAll(value, ",", "")
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		*warnings = append(*warnings, warning(field, IssueInvalidInteger))

		return nil
	}

	return &parsed
}

func parseNumber(raw, field string, warnings *[]Warning) *float64 {
	value := strings.TrimSpace(raw)
	if value == "" || isMissingValue(value) {
		return nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		*warnings = append(*warnings, warning(field, IssueInvalidNumber))

		return nil
	}

	return &parsed
}

func parseTimestamp(raw string) (time.Time, bool) {
	value := strings.TrimSpace(raw)
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05",
		"2006-01-02",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), true
		}
	}
	integer, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	var seconds, nanoseconds int64
	switch len(strings.TrimPrefix(value, "-")) {
	case 10:
		seconds = integer
	case 13:
		seconds, nanoseconds = integer/1e3, (integer%1e3)*1e6
	case 16:
		seconds, nanoseconds = integer/1e6, (integer%1e6)*1e3
	case 19:
		seconds, nanoseconds = integer/1e9, integer%1e9
	default:
		return time.Time{}, false
	}
	parsed := time.Unix(seconds, nanoseconds).UTC()
	if parsed.Year() < 2000 || parsed.Year() > 2200 {
		return time.Time{}, false
	}

	return parsed, true
}

func normalizeStringList(raw string) []string {
	values := expandList(raw)
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = cleanDisplay(value)
		key := strings.ToLower(value)
		if value == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}

	return result
}

func cleanIdentifier(raw string, lower bool) string {
	value := strings.TrimSpace(raw)
	if isMissingValue(value) {
		return ""
	}
	if lower {
		return strings.ToLower(value)
	}

	return value
}

func firstNonEmptyRaw(raw RawRecord, fields ...string) string {
	for _, field := range fields {
		if value := strings.TrimSpace(raw.Value(field)); value != "" {
			return value
		}
	}

	return ""
}

// isSuspiciousPhone reports whether normalized phone digits look like a
// keyboard placeholder: one repeated digit (0000000000, 1111111111, ...) or a
// consecutive ascending run such as 1234567890.
func isSuspiciousPhone(digits string) bool {
	if len(digits) < 7 {
		return false
	}

	repeated, sequential := true, true
	for index := 1; index < len(digits); index++ {
		if digits[index] != digits[0] {
			repeated = false
		}
		if digits[index] != '0'+byte((int(digits[index-1]-'0')+1)%10) {
			sequential = false
		}
	}

	return repeated || sequential
}

// isSuspiciousWebsite reports whether a website value is a documentation or
// loopback placeholder. The value itself is kept; only a warning is added.
func isSuspiciousWebsite(raw string, website URLValue) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "n/a", "none":
		return true
	}
	if !website.Valid {
		return false
	}

	switch website.Domain {
	case "example.com", "example.org", "localhost", "127.0.0.1":
		return true
	default:
		return false
	}
}

// isSuspiciousEmail reports whether a syntactically valid email is a
// placeholder: a documentation or test domain, or a no-reply sender.
func isSuspiciousEmail(email Email) bool {
	if email.Domain == "example.com" || email.Domain == "test.com" {
		return true
	}

	return strings.HasPrefix(email.Normalized, "noreply@")
}

func warning(field string, code IssueCode) Warning {
	return Warning{Field: field, Code: code, Message: warningMessage(code)}
}

func warningMessage(code IssueCode) string {
	switch code {
	case IssueExtraColumns:
		return "row contains values beyond the declared header"
	case IssueInvalidURL:
		return "value is not a valid HTTP or HTTPS URL"
	case IssueURLCredentials:
		return "URL credentials were rejected"
	case IssueInvalidPhone:
		return "one or more phone values could not be normalized"
	case IssueInvalidEmail:
		return "one or more email values failed syntax validation"
	case IssueInvalidInteger:
		return "value is not an integer"
	case IssueInvalidNumber:
		return "value is not a finite number"
	case IssueOutOfRange:
		return "numeric value is outside its supported range"
	case IssueInvalidJSON:
		return "structured value is not valid JSON"
	case IssueInvalidTimestamp:
		return "source timestamp could not be parsed"
	case IssueSuspiciousValue:
		return "value looks like a placeholder and needs review"
	case IssueDuplicateContact:
		return "duplicate normalized contact values were removed"
	case IssueUnsupportedScheme:
		return "URL scheme is not HTTP or HTTPS"
	case IssueMissingIdentity:
		return "row has no exact identity key"
	default:
		return "value needs review"
	}
}

func canonicalHeader(value string) string {
	value = strings.TrimPrefix(value, "\ufeff")
	value = strings.TrimSpace(strings.ToLower(value))
	var builder strings.Builder
	wasSeparator := false
	for _, char := range value {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			builder.WriteRune(char)
			wasSeparator = false
		} else if !wasSeparator && builder.Len() > 0 {
			builder.WriteByte('_')
			wasSeparator = true
		}
	}
	value = strings.Trim(builder.String(), "_")
	switch value {
	case "web_site", "url", "business_website":
		return "website"
	case "longtitude", "lng", "lon":
		return "longitude"
	case "lat":
		return "latitude"
	case "name", "business_name":
		return "title"
	case "description":
		return "descriptions"
	case "rating", "stars":
		return "review_rating"
	case "reviews_count", "reviews_total":
		return "review_count"
	case "query", "keyword", "search_query", "source_keyword":
		return "source_query"
	case "grid_cell", "cell", "cell_id", "grid_cell_id", "source_cell":
		return "source_grid_cell"
	case "scraped_at", "collected_at", "extracted_at", "observed_at", "source_at":
		return "source_timestamp"
	case "source_link":
		return "source_url"
	default:
		return value
	}
}

func sortStrings(values []string) {
	for index := 1; index < len(values); index++ {
		for cursor := index; cursor > 0 && values[cursor] < values[cursor-1]; cursor-- {
			values[cursor], values[cursor-1] = values[cursor-1], values[cursor]
		}
	}
}
