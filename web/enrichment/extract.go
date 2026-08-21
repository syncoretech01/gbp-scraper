package enrichment

import (
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/PuerkitoBio/goquery"
)

type rawEmail struct {
	address string
	source  Source
}

type rawPhone struct {
	value  string
	source Source
}

type rawAddress struct {
	address PostalAddress
	source  Source
}

type rawSocial struct {
	platform string
	url      string
	source   Source
}

type discoveredPage struct {
	url   string
	kind  PageKind
	score int
}

type extractedPage struct {
	page            PageResult
	emails          []rawEmail
	phones          []rawPhone
	socials         []rawSocial
	addresses       []rawAddress
	internalLinks   []string
	discovered      []discoveredPage
	technologies    []Detection
	trackers        []Detection
	parked          bool
	comingSoon      bool
	placeholder     bool
	templateSignals []string
	tlsVerified     bool
}

func extractPage(body []byte, pageURL string, kind PageKind) (extractedPage, error) {
	document, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return extractedPage{}, fmt.Errorf("parse HTML: %w", err)
	}

	parsedURL, err := url.Parse(pageURL)
	if err != nil {
		return extractedPage{}, fmt.Errorf("parse page URL: %w", err)
	}

	visibleDocument := document.Clone()
	visibleDocument.Find("script,style,noscript,template,svg").Remove()
	visibleText := collapseWhitespace(visibleDocument.Text())

	bodyHTML, err := document.Html()
	if err != nil {
		return extractedPage{}, fmt.Errorf("serialize HTML: %w", err)
	}

	page := PageResult{
		FinalURL:        pageURL,
		Kind:            kind,
		SizeBytes:       int64(len(body)),
		Title:           collapseWhitespace(document.Find("title").First().Text()),
		MetaDescription: metaContent(document, "description"),
		Language:        pageLanguage(document),
		MobileViewport:  hasMetaName(document, "viewport"),
		MixedContent:    hasMixedContent(document, parsedURL),
	}
	page.CopyrightYear = copyrightYear(visibleText)
	page.OldCopyright = page.CopyrightYear > 0 && page.CopyrightYear < time.Now().UTC().Year()-1

	source := Source{PageURL: pageURL, PageKind: kind}
	extracted := extractedPage{
		page:          page,
		emails:        extractEmails(document, visibleText, source),
		phones:        extractPhones(document, visibleText, source),
		socials:       extractSocialProfiles(document, parsedURL, source),
		addresses:     extractAddresses(document, source),
		internalLinks: extractInternalLinks(document, parsedURL),
		discovered:    discoverSupportingPages(document, parsedURL),
	}
	extracted.technologies, extracted.trackers = detectSignatures(bodyHTML, document)
	extracted.parked, extracted.comingSoon, extracted.placeholder, extracted.templateSignals =
		detectPlaceholderSignals(strings.ToLower(visibleText + " " + bodyHTML))

	return extracted, nil
}

func metaContent(document *goquery.Document, name string) string {
	var content string

	document.Find("meta").EachWithBreak(func(_ int, selection *goquery.Selection) bool {
		metaName, exists := selection.Attr("name")
		if !exists || !strings.EqualFold(strings.TrimSpace(metaName), name) {
			return true
		}

		content, _ = selection.Attr("content")
		content = collapseWhitespace(content)

		return false
	})

	return content
}

func hasMetaName(document *goquery.Document, name string) bool {
	found := false

	document.Find("meta").EachWithBreak(func(_ int, selection *goquery.Selection) bool {
		metaName, exists := selection.Attr("name")
		if exists && strings.EqualFold(strings.TrimSpace(metaName), name) {
			found = true
			return false
		}

		return true
	})

	return found
}

func pageLanguage(document *goquery.Document) string {
	language, _ := document.Find("html").First().Attr("lang")
	if language != "" {
		return strings.ToLower(strings.TrimSpace(language))
	}

	document.Find("meta").EachWithBreak(func(_ int, selection *goquery.Selection) bool {
		httpEquiv, exists := selection.Attr("http-equiv")
		if !exists || !strings.EqualFold(strings.TrimSpace(httpEquiv), "content-language") {
			return true
		}

		language, _ = selection.Attr("content")

		return false
	})

	return strings.ToLower(strings.TrimSpace(language))
}

func hasMixedContent(document *goquery.Document, pageURL *url.URL) bool {
	if !strings.EqualFold(pageURL.Scheme, "https") {
		return false
	}

	mixed := false

	document.Find("script[src],img[src],iframe[src],audio[src],video[src],source[src],link[href]").EachWithBreak(
		func(_ int, selection *goquery.Selection) bool {
			value, exists := selection.Attr("src")
			if !exists {
				value, exists = selection.Attr("href")
			}

			if exists && strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "http://") {
				mixed = true
				return false
			}

			return true
		},
	)

	return mixed
}

func extractEmails(document *goquery.Document, visibleText string, source Source) []rawEmail {
	findings := make([]rawEmail, 0)

	document.Find("a[href]").Each(func(_ int, selection *goquery.Selection) {
		href, exists := selection.Attr("href")
		if !exists || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(href)), "mailto:") {
			return
		}

		addressPart := strings.TrimSpace(href[len("mailto:"):])
		if question := strings.IndexByte(addressPart, '?'); question >= 0 {
			addressPart = addressPart[:question]
		}

		if decoded, err := url.PathUnescape(addressPart); err == nil {
			addressPart = decoded
		}

		for _, address := range splitAddresses(addressPart) {
			mailtoSource := source
			mailtoSource.Method = MethodMailto
			findings = append(findings, rawEmail{address: address, source: mailtoSource})
		}
	})

	for _, address := range findPlainEmails(visibleText) {
		textSource := source
		textSource.Method = MethodVisibleText
		findings = append(findings, rawEmail{address: address, source: textSource})
	}

	for _, address := range findDeobfuscatedEmails(visibleText) {
		obfuscatedSource := source
		obfuscatedSource.Method = MethodDeobfuscated
		findings = append(findings, rawEmail{address: address, source: obfuscatedSource})
	}

	document.Find("script").Each(func(_ int, selection *goquery.Selection) {
		scriptType, exists := selection.Attr("type")
		if !exists || !strings.EqualFold(strings.TrimSpace(scriptType), "application/ld+json") {
			return
		}

		for _, address := range findPlainEmails(selection.Text()) {
			structuredSource := source
			structuredSource.Method = MethodStructuredData
			findings = append(findings, rawEmail{address: address, source: structuredSource})
		}
	})

	return findings
}

func findPlainEmails(text string) []string {
	pattern := regexp.MustCompile(`(?i)[a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+`)

	matches := pattern.FindAllString(text, -1)
	for index := range matches {
		matches[index] = strings.Trim(matches[index], ".,;:!?")
	}

	return matches
}

func findDeobfuscatedEmails(text string) []string {
	pattern := regexp.MustCompile(`(?i)([a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+)\s*(?:\[at\]|\(at\))\s*([a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\s*(?:\.|\[dot\]|\(dot\))\s*[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+)`)
	matches := pattern.FindAllStringSubmatch(text, -1)
	addresses := make([]string, 0, len(matches))
	dotPattern := regexp.MustCompile(`(?i)\s*(?:\[dot\]|\(dot\)|\.)\s*`)

	for _, match := range matches {
		if len(match) != 3 {
			continue
		}

		domainParts := dotPattern.Split(strings.TrimSpace(match[2]), -1)
		addresses = append(addresses, strings.TrimSpace(match[1])+"@"+strings.Join(domainParts, "."))
	}

	return addresses
}

func splitAddresses(value string) []string {
	return strings.FieldsFunc(value, func(character rune) bool {
		return character == ',' || character == ';' || unicode.IsSpace(character)
	})
}

func extractPhones(document *goquery.Document, visibleText string, source Source) []rawPhone {
	findings := make([]rawPhone, 0)

	document.Find("a[href]").Each(func(_ int, selection *goquery.Selection) {
		href, exists := selection.Attr("href")
		if !exists || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(href)), "tel:") {
			return
		}

		value := strings.TrimSpace(href[len("tel:"):])
		if decoded, err := url.PathUnescape(value); err == nil {
			value = decoded
		}

		if normalized := normalizePhone(value); normalized != "" {
			telephoneSource := source
			telephoneSource.Method = MethodTelephoneLink
			findings = append(findings, rawPhone{value: normalized, source: telephoneSource})
		}
	})

	phonePattern := regexp.MustCompile(`(?:\+?\d[\d\s().-]{5,}\d)`)
	for _, match := range phonePattern.FindAllString(visibleText, -1) {
		if normalized := normalizePhone(match); normalized != "" {
			textSource := source
			textSource.Method = MethodVisibleText
			findings = append(findings, rawPhone{value: normalized, source: textSource})
		}
	}

	document.Find("script").Each(func(_ int, selection *goquery.Selection) {
		scriptType, exists := selection.Attr("type")
		if !exists || !strings.EqualFold(strings.TrimSpace(scriptType), "application/ld+json") {
			return
		}

		for _, match := range phonePattern.FindAllString(selection.Text(), -1) {
			if normalized := normalizePhone(match); normalized != "" {
				structuredSource := source
				structuredSource.Method = MethodStructuredData
				findings = append(findings, rawPhone{value: normalized, source: structuredSource})
			}
		}
	})

	return findings
}

func normalizePhone(value string) string {
	value = strings.TrimSpace(value)

	var normalized strings.Builder

	if strings.HasPrefix(value, "+") {
		normalized.WriteByte('+')
	}

	digitCount := 0

	for _, character := range value {
		if character >= '0' && character <= '9' {
			normalized.WriteRune(character)

			digitCount++
		}
	}

	if digitCount < 7 || digitCount > 15 {
		return ""
	}

	return normalized.String()
}

func extractSocialProfiles(document *goquery.Document, baseURL *url.URL, source Source) []rawSocial {
	profiles := make([]rawSocial, 0)

	document.Find("a[href]").Each(func(_ int, selection *goquery.Selection) {
		href, exists := selection.Attr("href")
		if !exists {
			return
		}

		profileURL, err := baseURL.Parse(strings.TrimSpace(href))
		if err != nil || (profileURL.Scheme != schemeHTTP && profileURL.Scheme != schemeHTTPS) {
			return
		}

		platform := socialPlatform(profileURL)
		if platform == "" || isSocialShareURL(profileURL) {
			return
		}

		standardizeSocialProfileURL(profileURL)
		socialSource := source
		socialSource.Method = MethodSocialLink
		profiles = append(profiles, rawSocial{
			platform: platform,
			url:      profileURL.String(),
			source:   socialSource,
		})
	})

	return profiles
}

func socialPlatform(profileURL *url.URL) string {
	host := strings.TrimPrefix(strings.ToLower(profileURL.Hostname()), "www.")

	switch {
	case host == "facebook.com" || strings.HasSuffix(host, ".facebook.com"):
		return "facebook"
	case host == "instagram.com" || strings.HasSuffix(host, ".instagram.com"):
		return "instagram"
	case host == "linkedin.com" || strings.HasSuffix(host, ".linkedin.com"):
		return "linkedin"
	case host == "x.com" || host == "twitter.com" || strings.HasSuffix(host, ".twitter.com"):
		return "x"
	case host == "youtube.com" || strings.HasSuffix(host, ".youtube.com") || host == "youtu.be":
		return "youtube"
	case host == "tiktok.com" || strings.HasSuffix(host, ".tiktok.com"):
		return "tiktok"
	case host == "wa.me" || host == "whatsapp.com" || strings.HasSuffix(host, ".whatsapp.com"):
		return "whatsapp"
	default:
		return ""
	}
}

// standardizeSocialProfileURL removes fragments and query tracking parameters
// from a social profile link while keeping meaningful path and query parts,
// such as facebook.com/profile.php?id=123. The same page shared from different
// campaigns then produces one stable profile URL.
func standardizeSocialProfileURL(profileURL *url.URL) {
	profileURL.Fragment = ""
	profileURL.RawFragment = ""

	query := profileURL.Query()
	for key := range query {
		if isSocialTrackingParameter(key) {
			delete(query, key)
		}
	}

	if len(query) == 0 {
		profileURL.RawQuery = ""
	} else {
		profileURL.RawQuery = query.Encode()
	}
}

func isSocialTrackingParameter(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if strings.HasPrefix(key, "utm_") {
		return true
	}

	switch key {
	case "fbclid", "gclid", "igshid", "ref", "ref_src", "mibextid", "share_id", "s", "feature":
		return true
	default:
		return false
	}
}

func isSocialShareURL(profileURL *url.URL) bool {
	path := strings.ToLower(profileURL.EscapedPath())
	return strings.Contains(path, "/share") || strings.Contains(path, "/sharer") ||
		strings.Contains(path, "/intent/")
}

func extractInternalLinks(document *goquery.Document, baseURL *url.URL) []string {
	links := make(map[string]struct{})

	document.Find("a[href]").Each(func(_ int, selection *goquery.Selection) {
		href, exists := selection.Attr("href")
		if !exists {
			return
		}

		linkURL, ok := resolveHTTPLink(baseURL, href)
		if !ok || !sameOrigin(baseURL, linkURL) {
			return
		}

		links[linkURL.String()] = struct{}{}
	})

	return sortedKeys(links)
}

func discoverSupportingPages(document *goquery.Document, baseURL *url.URL) []discoveredPage {
	candidates := make(map[string]discoveredPage)

	document.Find("a[href]").Each(func(_ int, selection *goquery.Selection) {
		href, exists := selection.Attr("href")
		if !exists {
			return
		}

		linkURL, ok := resolveHTTPLink(baseURL, href)
		if !ok || !sameOrigin(baseURL, linkURL) {
			return
		}

		kind, score := supportingPageKind(linkURL, selection.Text())
		if score == 0 {
			return
		}

		key := string(kind) + "|" + linkURL.String()
		candidate := discoveredPage{url: linkURL.String(), kind: kind, score: score}

		if current, found := candidates[key]; !found || candidate.score > current.score {
			candidates[key] = candidate
		}
	})

	result := make([]discoveredPage, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, candidate)
	}

	sort.Slice(result, func(left, right int) bool {
		if result[left].kind != result[right].kind {
			return result[left].kind < result[right].kind
		}

		if result[left].score != result[right].score {
			return result[left].score > result[right].score
		}

		return result[left].url < result[right].url
	})

	return result
}

func supportingPageKind(linkURL *url.URL, anchorText string) (kind PageKind, score int) {
	path := strings.ToLower(strings.Trim(linkURL.EscapedPath(), "/"))
	text := strings.ToLower(collapseWhitespace(anchorText))
	pathSegments := strings.FieldsFunc(path, func(character rune) bool {
		return character == '/' || character == '-' || character == '_'
	})

	if containsString(pathSegments, "contact") {
		return PageContact, 100 - minInt(len(path), 50)
	}

	if strings.Contains(text, "contact") || strings.Contains(text, "get in touch") {
		return PageContact, 70 - minInt(len(path), 30)
	}

	if containsString(pathSegments, "about") || strings.Contains(path, "about-us") {
		return PageAbout, 100 - minInt(len(path), 50)
	}

	if strings.Contains(text, "about") || strings.Contains(text, "our story") ||
		strings.Contains(text, "who we are") {
		return PageAbout, 70 - minInt(len(path), 30)
	}

	return "", 0
}

func resolveHTTPLink(baseURL *url.URL, href string) (*url.URL, bool) {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return nil, false
	}

	linkURL, err := baseURL.Parse(href)
	if err != nil {
		return nil, false
	}

	linkURL.Scheme = strings.ToLower(linkURL.Scheme)
	if linkURL.Scheme != schemeHTTP && linkURL.Scheme != schemeHTTPS {
		return nil, false
	}

	linkURL.Fragment = ""

	return linkURL, true
}

func sameOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(left.Hostname(), right.Hostname()) &&
		effectivePort(left) == effectivePort(right)
}

func effectivePort(parsedURL *url.URL) string {
	if parsedURL.Port() != "" {
		return parsedURL.Port()
	}

	if strings.EqualFold(parsedURL.Scheme, schemeHTTPS) {
		return "443"
	}

	return "80"
}

func copyrightYear(text string) int {
	pattern := regexp.MustCompile(`(?i)(?:©|copyright)\s*(?:\d{4}\s*[-–]\s*)?(\d{4})`)
	year := 0

	for _, match := range pattern.FindAllStringSubmatch(text, -1) {
		if len(match) != 2 {
			continue
		}

		parsedYear, err := strconv.Atoi(match[1])
		if err == nil && parsedYear > year {
			year = parsedYear
		}
	}

	return year
}

func collapseWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}

	return false
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}

func minInt(left, right int) int {
	if left < right {
		return left
	}

	return right
}
