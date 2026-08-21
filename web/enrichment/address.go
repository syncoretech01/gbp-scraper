package enrichment

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// maximumAddressLength bounds one extracted address so a page that wraps a
// whole paragraph in an <address> element cannot store an essay.
const maximumAddressLength = 300

// maximumAddressesPerPage bounds how many addresses one page contributes.
const maximumAddressesPerPage = 5

// postalAddressFields are the schema.org property names an address object may
// carry. They are matched case-insensitively, because real pages use every
// casing.
var postalAddressFields = map[string]string{
	"streetaddress":   "street",
	"addresslocality": "locality",
	"addressregion":   "region",
	"postalcode":      "postal_code",
	"addresscountry":  "country",
}

// addressBlockPattern recognises a visible address line: a street number
// followed by words and, somewhere after it, a postal-code-shaped token. It is
// intentionally conservative — a line that does not look like an address is
// left out rather than guessed at.
var addressBlockPattern = regexp.MustCompile(
	`(?i)\d{1,6}[\p{L}\-]*\s+[\p{L}][\p{L}.'\-]*(?:\s+[\p{L}][\p{L}.'\-]*){0,6}.{0,60}?\b[A-Z0-9][A-Z0-9\- ]{2,9}\b`,
)

// extractAddresses finds postal addresses on one page using structured data
// first, then microdata, then the semantic <address> element. Free prose is
// never scanned: a page's body text is far too noisy to yield an address a
// user would trust.
func extractAddresses(document *goquery.Document, source Source) []rawAddress {
	found := make([]rawAddress, 0, maximumAddressesPerPage)
	seen := make(map[string]struct{}, maximumAddressesPerPage)

	appendAddress := func(address PostalAddress, method ExtractionMethod) {
		address.Value = trimAddressValue(address.Value)
		if address.Value == "" {
			address.Value = composeAddressValue(address)
		}

		if address.Value == "" || len(found) >= maximumAddressesPerPage {
			return
		}

		key := addressKey(address.Value)
		if _, duplicate := seen[key]; duplicate {
			return
		}

		seen[key] = struct{}{}
		addressSource := source
		addressSource.Method = method
		found = append(found, rawAddress{address: address, source: addressSource})
	}

	for _, address := range structuredDataAddresses(document) {
		appendAddress(address, MethodStructuredData)
	}

	for _, address := range microdataAddresses(document) {
		appendAddress(address, MethodMicrodata)
	}

	for _, address := range semanticAddressElements(document) {
		appendAddress(address, MethodVisibleText)
	}

	return found
}

// structuredDataAddresses walks every JSON-LD block looking for schema.org
// PostalAddress objects, wherever they are nested.
func structuredDataAddresses(document *goquery.Document) []PostalAddress {
	addresses := make([]PostalAddress, 0)

	document.Find("script").Each(func(_ int, selection *goquery.Selection) {
		scriptType, exists := selection.Attr("type")
		if !exists || !strings.EqualFold(strings.TrimSpace(scriptType), "application/ld+json") {
			return
		}

		var payload any
		if err := json.Unmarshal([]byte(selection.Text()), &payload); err != nil {
			return
		}

		collectStructuredAddresses(payload, &addresses, 0)
	})

	return addresses
}

// maximumStructuredDepth bounds the JSON-LD walk so a deliberately nested
// document cannot cost unbounded work.
const maximumStructuredDepth = 8

func collectStructuredAddresses(value any, addresses *[]PostalAddress, depth int) {
	if depth > maximumStructuredDepth || len(*addresses) >= maximumAddressesPerPage {
		return
	}

	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			collectStructuredAddresses(item, addresses, depth+1)
		}
	case map[string]any:
		if address, ok := structuredAddressObject(typed); ok {
			*addresses = append(*addresses, address)
		}

		for _, item := range typed {
			collectStructuredAddresses(item, addresses, depth+1)
		}
	}
}

// structuredAddressObject converts one JSON-LD object into an address when it
// carries at least one recognised postal field.
func structuredAddressObject(object map[string]any) (PostalAddress, bool) {
	address := PostalAddress{}
	fields := 0

	for key, raw := range object {
		field, recognised := postalAddressFields[strings.ToLower(strings.TrimSpace(key))]
		if !recognised {
			continue
		}

		text := structuredAddressText(raw)
		if text == "" {
			continue
		}

		fields++
		assignAddressField(&address, field, text)
	}

	if fields == 0 {
		return PostalAddress{}, false
	}

	return address, true
}

// structuredAddressText flattens the scalar forms a JSON-LD value may take.
func structuredAddressText(value any) string {
	switch typed := value.(type) {
	case string:
		return collapseWhitespace(typed)
	case map[string]any:
		// addressCountry is often {"@type":"Country","name":"US"}.
		if name, ok := typed["name"].(string); ok {
			return collapseWhitespace(name)
		}
	case []any:
		if len(typed) > 0 {
			return structuredAddressText(typed[0])
		}
	}

	return ""
}

// microdataAddresses reads schema.org microdata properties from the markup.
func microdataAddresses(document *goquery.Document) []PostalAddress {
	addresses := make([]PostalAddress, 0)

	document.Find(`[itemtype*="PostalAddress" i]`).EachWithBreak(func(_ int, selection *goquery.Selection) bool {
		if len(addresses) >= maximumAddressesPerPage {
			return false
		}

		address := PostalAddress{}
		fields := 0

		selection.Find("[itemprop]").Each(func(_ int, property *goquery.Selection) {
			name, exists := property.Attr("itemprop")
			if !exists {
				return
			}

			field, recognised := postalAddressFields[strings.ToLower(strings.TrimSpace(name))]
			if !recognised {
				return
			}

			text := collapseWhitespace(property.Text())
			if text == "" {
				text = collapseWhitespace(property.AttrOr("content", ""))
			}

			if text == "" {
				return
			}

			fields++
			assignAddressField(&address, field, text)
		})

		if fields > 0 {
			addresses = append(addresses, address)
		}

		return true
	})

	return addresses
}

// semanticAddressElements reads the <address> element, which is the one place
// HTML itself declares a postal address.
func semanticAddressElements(document *goquery.Document) []PostalAddress {
	addresses := make([]PostalAddress, 0)

	document.Find("address").EachWithBreak(func(_ int, selection *goquery.Selection) bool {
		if len(addresses) >= maximumAddressesPerPage {
			return false
		}

		text := collapseWhitespace(selection.Text())
		if !looksLikePostalAddress(text) {
			return true
		}

		addresses = append(addresses, PostalAddress{Value: text})

		return true
	})

	return addresses
}

// looksLikePostalAddress keeps obviously non-postal <address> contents (an
// email-only byline, a single word) out of the result.
func looksLikePostalAddress(text string) bool {
	text = strings.TrimSpace(text)
	if len(text) < 8 || len(text) > maximumAddressLength {
		return false
	}

	if len(strings.Fields(text)) < 3 {
		return false
	}

	return addressBlockPattern.MatchString(text)
}

func assignAddressField(address *PostalAddress, field, value string) {
	switch field {
	case "street":
		address.Street = value
	case "locality":
		address.Locality = value
	case "region":
		address.Region = value
	case "postal_code":
		address.PostalCode = value
	case "country":
		address.Country = value
	}
}

// composeAddressValue builds a display value from the structured parts when
// the source did not provide one.
func composeAddressValue(address PostalAddress) string {
	parts := make([]string, 0, 5)
	for _, part := range []string{
		address.Street, address.Locality, address.Region, address.PostalCode, address.Country,
	} {
		if strings.TrimSpace(part) != "" {
			parts = append(parts, strings.TrimSpace(part))
		}
	}

	return trimAddressValue(strings.Join(parts, ", "))
}

func trimAddressValue(value string) string {
	value = collapseWhitespace(value)
	if len(value) > maximumAddressLength {
		return ""
	}

	return value
}

// addressKey is the comparison form used to drop duplicates found by more
// than one method.
func addressKey(value string) string {
	var builder strings.Builder

	for _, character := range strings.ToLower(value) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			builder.WriteRune(character)
		}
	}

	return builder.String()
}

// mergeAddresses folds per-page findings into one ordered, deduplicated list
// with every page that reported each address.
func mergeAddresses(findings []rawAddress) []PostalAddress {
	merged := make(map[string]*PostalAddress)
	order := make([]string, 0, len(findings))

	for _, finding := range findings {
		key := addressKey(finding.address.Value)
		if key == "" {
			continue
		}

		address, found := merged[key]
		if !found {
			copied := finding.address
			copied.Sources = nil
			merged[key] = &copied
			order = append(order, key)
			address = &copied
		}

		// A later, more structured finding fills gaps the first one left.
		fillAddressGaps(address, finding.address)
		address.Sources = appendUniqueSource(address.Sources, finding.source)
	}

	result := make([]PostalAddress, 0, len(order))

	for _, key := range order {
		address := merged[key]
		sortSources(address.Sources)
		result = append(result, *address)
	}

	sort.SliceStable(result, func(left, right int) bool {
		return result[left].Value < result[right].Value
	})

	return result
}

func fillAddressGaps(destination *PostalAddress, source PostalAddress) {
	if destination.Street == "" {
		destination.Street = source.Street
	}

	if destination.Locality == "" {
		destination.Locality = source.Locality
	}

	if destination.Region == "" {
		destination.Region = source.Region
	}

	if destination.PostalCode == "" {
		destination.PostalCode = source.PostalCode
	}

	if destination.Country == "" {
		destination.Country = source.Country
	}
}
