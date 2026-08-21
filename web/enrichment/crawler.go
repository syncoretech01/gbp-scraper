package enrichment

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	defaultTimeout                     = 10 * time.Second
	defaultMaxPages                    = 3
	defaultMaxBodyBytes          int64 = 2 * 1024 * 1024
	defaultMaxRedirects                = 10
	defaultMaxInternalLinkChecks       = 10
	defaultUserAgent                   = "GoogleMapsScraper-Local-Enrichment/1.0"
)

// Crawler performs bounded website and contact analysis.
type Crawler struct {
	config Config
	guard  URLGuard
	client *http.Client
}

// NewCrawler validates configuration and creates a bounded crawler. Its
// default transport pins requests to DNS addresses that passed URLGuard.
func NewCrawler(config Config) (*Crawler, error) { //nolint:gocritic // A copy isolates runtime limits from caller mutation.
	if config.Timeout < 0 {
		return nil, errors.New("timeout cannot be negative")
	}

	if config.MaxPages < 0 {
		return nil, errors.New("maximum pages cannot be negative")
	}

	if config.MaxBodyBytes < 0 {
		return nil, errors.New("maximum body bytes cannot be negative")
	}

	if config.MaxRedirects < 0 {
		return nil, errors.New("maximum redirects cannot be negative")
	}

	if config.MaxInternalLinkChecks < 0 {
		return nil, errors.New("maximum internal link checks cannot be negative")
	}

	if config.Timeout == 0 {
		config.Timeout = defaultTimeout
	}

	if config.MaxPages == 0 {
		config.MaxPages = defaultMaxPages
	}

	if config.MaxBodyBytes == 0 {
		config.MaxBodyBytes = defaultMaxBodyBytes
	}

	if config.MaxRedirects == 0 {
		config.MaxRedirects = defaultMaxRedirects
	}

	if config.MaxInternalLinkChecks == 0 && !config.DisableInternalLinkChecks {
		config.MaxInternalLinkChecks = defaultMaxInternalLinkChecks
	}

	if config.Scope == "" {
		config.Scope = ScopeContactAbout
	}

	if config.Scope != ScopeHomepage && config.Scope != ScopeContact && config.Scope != ScopeContactAbout {
		return nil, fmt.Errorf("unsupported crawl scope %q", config.Scope)
	}

	if strings.TrimSpace(config.UserAgent) == "" {
		config.UserAgent = defaultUserAgent
	}

	guard := URLGuard{
		Resolver:                  config.Resolver,
		UnsafeAllowPrivateNetwork: config.UnsafeAllowPrivateNetwork,
	}
	client := config.HTTPClient

	if client == nil {
		transport := &http.Transport{
			DialContext:           guard.dialContext,
			ForceAttemptHTTP2:     true,
			TLSHandshakeTimeout:   config.Timeout,
			ResponseHeaderTimeout: config.Timeout,
		}
		client = &http.Client{Transport: transport, Timeout: config.Timeout}
	} else {
		clientCopy := *client
		clientCopy.Timeout = config.Timeout
		client = &clientCopy
	}

	return &Crawler{config: config, guard: guard, client: client}, nil
}

// Analyze crawls a homepage and selected same-origin supporting pages, then
// returns local contact, quality, technology, and reachability signals.
func (c *Crawler) Analyze(ctx context.Context, rawURL string) (Result, error) {
	validatedURL, err := c.guard.ValidateURL(ctx, rawURL)
	if err != nil {
		return Result{}, err
	}

	result := Result{RequestedURL: validatedURL.String()}
	allEmails := make([]rawEmail, 0)
	allPhones := make([]rawPhone, 0)
	allSocials := make([]rawSocial, 0)
	allAddresses := make([]rawAddress, 0)
	internalLinks := make(map[string]struct{})
	fetchedURLs := make(map[string]struct{})
	technologies := make(map[string]Detection)
	trackers := make(map[string]Detection)
	indicators := make(map[string]struct{})

	homepage, fetchErr := c.fetchPage(ctx, validatedURL.String(), PageHomepage)
	result.Pages = append(result.Pages, homepage.page)

	if fetchErr != nil {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}

		result.Error = fetchErr.Error()
		result.CertificateError = certificateError(fetchErr)

		if errors.Is(fetchErr, ErrUnsafeURL) || errors.Is(fetchErr, ErrUnsupportedScheme) {
			return result, fetchErr
		}

		return result, nil
	}

	mergeExtractedPage(
		&result,
		&homepage,
		&allEmails,
		&allPhones,
		&allSocials,
		&allAddresses,
		internalLinks,
		fetchedURLs,
		technologies,
		trackers,
		indicators,
	)

	result.Reachable = true
	result.FinalURL = homepage.page.FinalURL
	result.StatusCode = homepage.page.StatusCode
	result.ResponseTime = homepage.page.ResponseTime
	result.RedirectChain = append([]Redirect(nil), homepage.page.Redirects...)
	result.HTTPS = strings.HasPrefix(strings.ToLower(result.FinalURL), schemeHTTPS+"://")
	result.TLSValid = homepage.tlsVerified

	supportingPages := selectSupportingPages(homepage.discovered, c.config.Scope)
	for _, selectedPage := range supportingPages {
		if len(result.Pages) >= c.config.MaxPages {
			break
		}

		if _, alreadyFetched := fetchedURLs[selectedPage.url]; alreadyFetched {
			continue
		}

		page, pageErr := c.fetchPage(ctx, selectedPage.url, selectedPage.kind)
		result.Pages = append(result.Pages, page.page)

		if pageErr != nil {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}

			continue
		}

		mergeExtractedPage(
			&result,
			&page,
			&allEmails,
			&allPhones,
			&allSocials,
			&allAddresses,
			internalLinks,
			fetchedURLs,
			technologies,
			trackers,
			indicators,
		)
	}

	emailConfig := EmailAnalysisConfig{
		WebsiteURL:        result.FinalURL,
		CheckMX:           c.config.CheckMX,
		MXLookup:          c.config.MXLookup,
		DisposableDomains: c.config.DisposableDomains,
	}

	result.Emails, err = analyzeRawEmails(ctx, allEmails, emailConfig)
	if err != nil {
		return Result{}, err
	}

	result.Phones = mergePhones(allPhones)
	result.SocialProfiles = mergeSocialProfiles(allSocials)
	result.Addresses = mergeAddresses(allAddresses)
	result.Technologies = sortedDetections(technologies)
	result.Trackers = sortedDetections(trackers)
	result.TemplateIndicators = sortedKeys(indicators)

	if !c.config.DisableInternalLinkChecks && c.config.MaxInternalLinkChecks > 0 {
		checks, checkErr := c.checkInternalLinks(ctx, internalLinks, fetchedURLs)
		if checkErr != nil {
			return Result{}, checkErr
		}

		result.InternalLinksChecked = len(checks)

		for _, check := range checks {
			if check.Broken {
				result.BrokenInternalLinkCount++
				result.BrokenInternalLinks = append(result.BrokenInternalLinks, check)
			}
		}
	}

	result.ContentAudit = auditContent(&result)

	return result, nil
}

// visibleContactMethods are the extraction methods that mean a human reading
// the page can see the value, as opposed to it only existing in markup a
// crawler parsed.
var visibleContactMethods = map[ExtractionMethod]struct{}{
	MethodMailto:        {},
	MethodVisibleText:   {},
	MethodDeobfuscated:  {},
	MethodTelephoneLink: {},
}

// auditContent records which basic quality elements the crawl actually found.
// It looks only at evidence already gathered, so it costs no extra request.
func auditContent(result *Result) ContentAudit {
	audit := ContentAudit{}

	for _, page := range result.Pages {
		switch page.Kind {
		case PageContact:
			audit.ContactPage = audit.ContactPage || page.StatusCode > 0 && page.StatusCode < 400
		case PageAbout:
			audit.AboutPage = audit.AboutPage || page.StatusCode > 0 && page.StatusCode < 400
		case PageHomepage:
			audit.PageTitle = audit.PageTitle || strings.TrimSpace(page.Title) != ""
			audit.MetaDescription = audit.MetaDescription || strings.TrimSpace(page.MetaDescription) != ""
			audit.MobileViewport = audit.MobileViewport || page.MobileViewport
		}
	}

	for _, phone := range result.Phones {
		if hasVisibleSource(phone.Sources) {
			audit.VisiblePhone = true

			break
		}
	}

	for _, email := range result.Emails {
		if hasVisibleSource(email.Sources) {
			audit.VisibleEmail = true

			break
		}
	}

	audit.PostalAddress = len(result.Addresses) > 0
	audit.SocialLinks = len(result.SocialProfiles) > 0

	for _, present := range []bool{
		audit.ContactPage, audit.AboutPage, audit.VisiblePhone, audit.VisibleEmail,
		audit.PostalAddress, audit.SocialLinks, audit.PageTitle, audit.MetaDescription,
		audit.MobileViewport,
	} {
		audit.Checked++

		if present {
			audit.Present++
		}
	}

	return audit
}

func hasVisibleSource(sources []Source) bool {
	for _, source := range sources {
		if _, visible := visibleContactMethods[source.Method]; visible {
			return true
		}
	}

	return false
}

func (c *Crawler) fetchPage(ctx context.Context, rawURL string, kind PageKind) (extractedPage, error) {
	page := PageResult{RequestedURL: rawURL, Kind: kind}
	validatedURL, err := c.guard.ValidateURL(ctx, rawURL)

	if err != nil {
		page.Error = err.Error()
		return extractedPage{page: page}, err
	}

	response, redirects, elapsed, err := c.request(ctx, http.MethodGet, validatedURL.String())
	page.ResponseTime = elapsed
	page.Redirects = redirects

	if err != nil {
		page.Error = err.Error()
		return extractedPage{page: page}, err
	}

	defer response.Body.Close()

	page.FinalURL = response.Request.URL.String()
	page.StatusCode = response.StatusCode
	page.ContentType = response.Header.Get("Content-Type")
	body, readErr := io.ReadAll(io.LimitReader(response.Body, c.config.MaxBodyBytes+1))

	if readErr != nil {
		page.Error = readErr.Error()
		return extractedPage{page: page}, fmt.Errorf("read response body: %w", readErr)
	}

	if int64(len(body)) > c.config.MaxBodyBytes {
		page.BodyTruncated = true
		body = body[:c.config.MaxBodyBytes]
	}

	page.SizeBytes = int64(len(body))

	extracted := extractedPage{page: page}
	if response.TLS != nil && len(response.TLS.VerifiedChains) > 0 {
		extracted.tlsVerified = true
	}

	if !isHTMLContent(page.ContentType, body) {
		return extracted, nil
	}

	extracted, err = extractPage(body, page.FinalURL, kind)
	if err != nil {
		page.Error = err.Error()
		extracted.page = page

		return extracted, nil
	}

	extracted.page.RequestedURL = page.RequestedURL
	extracted.page.FinalURL = page.FinalURL
	extracted.page.StatusCode = page.StatusCode
	extracted.page.ResponseTime = page.ResponseTime
	extracted.page.SizeBytes = page.SizeBytes
	extracted.page.BodyTruncated = page.BodyTruncated
	extracted.page.ContentType = page.ContentType
	extracted.page.Redirects = page.Redirects
	extracted.tlsVerified = response.TLS != nil && len(response.TLS.VerifiedChains) > 0

	return extracted, nil
}

func (c *Crawler) request(
	ctx context.Context,
	method string,
	rawURL string,
) (*http.Response, []Redirect, time.Duration, error) {
	request, err := http.NewRequestWithContext(ctx, method, rawURL, http.NoBody)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("create request: %w", err)
	}

	request.Header.Set("User-Agent", c.config.UserAgent)

	redirects := make([]Redirect, 0)
	clientCopy := *c.client
	originalRedirectPolicy := c.client.CheckRedirect
	clientCopy.CheckRedirect = func(nextRequest *http.Request, via []*http.Request) error {
		if len(via) > c.config.MaxRedirects {
			return fmt.Errorf("maximum redirects exceeded (%d)", c.config.MaxRedirects)
		}

		if _, validationErr := c.guard.ValidateURL(nextRequest.Context(), nextRequest.URL.String()); validationErr != nil {
			return validationErr
		}

		if nextRequest.Response != nil && nextRequest.Response.Request != nil {
			redirects = append(redirects, Redirect{
				From:       nextRequest.Response.Request.URL.String(),
				To:         nextRequest.URL.String(),
				StatusCode: nextRequest.Response.StatusCode,
			})
		}

		if originalRedirectPolicy != nil {
			return originalRedirectPolicy(nextRequest, via)
		}

		return nil
	}

	startedAt := time.Now()
	response, err := clientCopy.Do(request)
	elapsed := time.Since(startedAt)

	if err != nil {
		return nil, redirects, elapsed, fmt.Errorf("request %s: %w", rawURL, err)
	}

	return response, redirects, elapsed, nil
}

func (c *Crawler) checkInternalLinks(
	ctx context.Context,
	internalLinks map[string]struct{},
	fetchedURLs map[string]struct{},
) ([]LinkCheck, error) {
	urls := sortedKeys(internalLinks)
	checks := make([]LinkCheck, 0, minInt(len(urls), c.config.MaxInternalLinkChecks))

	for _, rawURL := range urls {
		if len(checks) >= c.config.MaxInternalLinkChecks {
			break
		}

		if _, fetched := fetchedURLs[rawURL]; fetched {
			continue
		}

		if err := ctx.Err(); err != nil {
			return nil, err
		}

		check := LinkCheck{URL: rawURL}

		response, _, _, err := c.request(ctx, http.MethodHead, rawURL)
		if err == nil && (response.StatusCode == http.StatusMethodNotAllowed || response.StatusCode == http.StatusNotImplemented) {
			response.Body.Close()
			response, _, _, err = c.request(ctx, http.MethodGet, rawURL)
		}

		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, contextErr
			}

			check.Broken = true
			check.Error = err.Error()
		} else {
			check.StatusCode = response.StatusCode
			check.Broken = response.StatusCode >= http.StatusBadRequest
			response.Body.Close()
		}

		checks = append(checks, check)
	}

	return checks, nil
}

func mergeExtractedPage(
	result *Result,
	page *extractedPage,
	emails *[]rawEmail,
	phones *[]rawPhone,
	socials *[]rawSocial,
	addresses *[]rawAddress,
	internalLinks map[string]struct{},
	fetchedURLs map[string]struct{},
	technologies map[string]Detection,
	trackers map[string]Detection,
	indicators map[string]struct{},
) {
	*emails = append(*emails, page.emails...)
	*phones = append(*phones, page.phones...)
	*socials = append(*socials, page.socials...)
	*addresses = append(*addresses, page.addresses...)

	if page.page.FinalURL != "" {
		fetchedURLs[page.page.FinalURL] = struct{}{}
	}

	for _, link := range page.internalLinks {
		internalLinks[link] = struct{}{}
	}

	mergeDetectionMap(technologies, page.technologies)
	mergeDetectionMap(trackers, page.trackers)

	for _, indicator := range page.templateSignals {
		indicators[indicator] = struct{}{}
	}

	result.MixedContent = result.MixedContent || page.page.MixedContent
	result.Parked = result.Parked || page.parked
	result.ComingSoon = result.ComingSoon || page.comingSoon
	result.Placeholder = result.Placeholder || page.placeholder
}

func selectSupportingPages(discovered []discoveredPage, scope CrawlScope) []discoveredPage {
	if scope == ScopeHomepage {
		return nil
	}

	selected := make([]discoveredPage, 0, 2)
	selectedKinds := make(map[PageKind]struct{})
	selectedURLs := make(map[string]struct{})

	for _, candidate := range discovered {
		if candidate.kind == PageAbout && scope != ScopeContactAbout {
			continue
		}

		if _, found := selectedKinds[candidate.kind]; found {
			continue
		}

		if _, found := selectedURLs[candidate.url]; found {
			continue
		}

		selected = append(selected, candidate)
		selectedKinds[candidate.kind] = struct{}{}
		selectedURLs[candidate.url] = struct{}{}
	}

	return selected
}

func mergePhones(findings []rawPhone) []Phone {
	values := make(map[string]*Phone)
	for _, finding := range findings {
		phone, found := values[finding.value]
		if !found {
			phone = &Phone{Value: finding.value}
			values[finding.value] = phone
		}

		phone.Sources = appendUniqueSource(phone.Sources, finding.source)
	}

	result := make([]Phone, 0, len(values))

	for _, phone := range values {
		sortSources(phone.Sources)
		result = append(result, *phone)
	}

	sort.Slice(result, func(left, right int) bool {
		return result[left].Value < result[right].Value
	})

	return result
}

func mergeSocialProfiles(findings []rawSocial) []SocialProfile {
	profiles := make(map[string]*SocialProfile)

	for _, finding := range findings {
		key := finding.platform + "|" + finding.url

		profile, found := profiles[key]
		if !found {
			profile = &SocialProfile{Platform: finding.platform, URL: finding.url}
			profiles[key] = profile
		}

		profile.Sources = appendUniqueSource(profile.Sources, finding.source)
	}

	result := make([]SocialProfile, 0, len(profiles))

	for _, profile := range profiles {
		sortSources(profile.Sources)
		result = append(result, *profile)
	}

	sort.Slice(result, func(left, right int) bool {
		if result[left].Platform != result[right].Platform {
			return result[left].Platform < result[right].Platform
		}

		return result[left].URL < result[right].URL
	})

	return result
}

func mergeDetectionMap(destination map[string]Detection, values []Detection) {
	for _, value := range values {
		current, found := destination[value.Name]
		if !found || value.Confidence > current.Confidence {
			current.Confidence = value.Confidence
		}

		current.Name = value.Name
		evidence := make(map[string]struct{})

		for _, item := range current.Evidence {
			evidence[item] = struct{}{}
		}

		for _, item := range value.Evidence {
			evidence[item] = struct{}{}
		}

		current.Evidence = sortedKeys(evidence)
		destination[value.Name] = current
	}
}

func sortedDetections(values map[string]Detection) []Detection {
	result := make([]Detection, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}

	sort.Slice(result, func(left, right int) bool {
		return result[left].Name < result[right].Name
	})

	return result
}

func isHTMLContent(contentType string, body []byte) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err == nil && mediaType != "" {
		return mediaType == "text/html" || mediaType == "application/xhtml+xml"
	}

	detected := http.DetectContentType(body)

	return strings.HasPrefix(detected, "text/html")
}

func certificateError(err error) string {
	if err == nil {
		return ""
	}

	message := strings.ToLower(err.Error())
	if strings.Contains(message, "x509") || strings.Contains(message, "certificate") ||
		strings.Contains(message, "tls") {
		return err.Error()
	}

	return ""
}
