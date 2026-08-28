package enrichment

import (
	"context"
	"errors"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

type mxResult struct {
	status  MXStatus
	records []string
	err     string
}

// AnalyzeEmails validates, classifies, optionally checks MX records, and
// deterministically ranks extracted email candidates.
func AnalyzeEmails(
	ctx context.Context,
	candidates []EmailCandidate,
	config EmailAnalysisConfig,
) ([]Email, error) {
	findings := make([]rawEmail, 0, len(candidates))
	for _, candidate := range candidates {
		findings = append(findings, rawEmail{address: candidate.Address, source: candidate.Source})
	}

	emails, _, err := analyzeRawEmails(ctx, findings, config)

	return emails, err
}

// analyzeRawEmails turns raw candidates into ranked addresses and returns the
// funnel that explains what happened to every candidate it was given.
func analyzeRawEmails(
	ctx context.Context,
	findings []rawEmail,
	config EmailAnalysisConfig,
) ([]Email, EmailFunnel, error) {
	disposableDomains := defaultDisposableDomains()

	for _, domain := range config.DisposableDomains {
		domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
		if domain != "" {
			disposableDomains[domain] = struct{}{}
		}
	}

	emails := make(map[string]*Email)
	funnel := EmailFunnel{Discovered: len(findings)}
	// judged remembers which raw candidates hygiene has already ruled on, so a
	// repeated occurrence is counted once rather than once per page.
	judged := make(map[string]string, len(findings))

	for _, finding := range findings {
		_, alreadyJudged := judged[finding.address]
		if !alreadyJudged {
			funnel.Distinct++
		}

		sanitized, reason, ok := sanitizeEmailCandidate(finding.address)
		if !ok {
			if !alreadyJudged {
				judged[finding.address] = ""
				funnel.reject(reason)
			}

			continue
		}

		address, domain, valid := normalizeEmailAddress(sanitized.address)
		if address == "" || !valid {
			if !alreadyJudged {
				judged[finding.address] = ""
				funnel.reject(RejectionSyntax)
			}

			continue
		}

		if !alreadyJudged {
			judged[finding.address] = address
			if sanitized.repaired {
				funnel.Repaired++
			}
		}

		key := strings.ToLower(address)

		current, found := emails[key]
		if !found {
			role, roleAddress := classifyRole(address)
			current = &Email{
				Address:        address,
				Domain:         domain,
				ValidSyntax:    valid,
				Role:           role,
				RoleAddress:    roleAddress,
				PersonalLikely: valid && !roleAddress && personalLooking(address),
				Disposable:     valid && disposableDomain(domain, disposableDomains),
				MXStatus:       MXNotChecked,
			}
			emails[key] = current
		}

		current.Sources = appendUniqueSource(current.Sources, finding.source)
	}

	websiteDomain := websiteHost(config.WebsiteURL)
	mxCache := make(map[string]mxResult)

	for _, email := range emails {
		if err := ctx.Err(); err != nil {
			return nil, EmailFunnel{}, err
		}

		if config.CheckMX && email.ValidSyntax {
			lookupResult, found := mxCache[email.Domain]
			if !found {
				lookupResult = lookupMX(ctx, email.Domain, config.MXLookup)
				mxCache[email.Domain] = lookupResult
			}

			email.MXStatus = lookupResult.status
			email.MXRecords = append([]string(nil), lookupResult.records...)
			email.MXError = lookupResult.err
		}

		sortSources(email.Sources)
		email.Relevance = emailRelevance(email, websiteDomain)
	}

	result := make([]Email, 0, len(emails))
	for _, email := range emails {
		result = append(result, *email)
	}

	sort.Slice(result, func(left, right int) bool {
		if result[left].Relevance != result[right].Relevance {
			return result[left].Relevance > result[right].Relevance
		}

		return result[left].Address < result[right].Address
	})

	for index := range result {
		result[index].Rank = index + 1
	}

	funnel.Accepted = len(result)

	return result, funnel, nil
}

func normalizeEmailAddress(rawAddress string) (address, domain string, valid bool) {
	rawAddress = strings.TrimSpace(strings.Trim(rawAddress, "<>\"'.,;:"))
	if rawAddress == "" || strings.Count(rawAddress, "@") != 1 {
		return strings.ToLower(rawAddress), "", false
	}

	parts := strings.SplitN(rawAddress, "@", 2)
	localPart := strings.TrimSpace(parts[0])
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(parts[1])), ".")

	if localPart == "" || len(localPart) > 64 || domain == "" {
		return strings.ToLower(rawAddress), domain, false
	}

	asciiDomain, err := idna.Lookup.ToASCII(domain)
	if err != nil {
		return strings.ToLower(localPart) + "@" + domain, domain, false
	}

	asciiDomain = strings.ToLower(asciiDomain)
	address = strings.ToLower(localPart) + "@" + asciiDomain

	if len(address) > 254 || !validLocalPart(localPart) || !validDomain(asciiDomain) {
		return address, asciiDomain, false
	}

	return address, asciiDomain, true
}

func validLocalPart(localPart string) bool {
	pattern := regexp.MustCompile(`^[A-Za-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+$`)
	return pattern.MatchString(localPart) && !strings.HasPrefix(localPart, ".") &&
		!strings.HasSuffix(localPart, ".") && !strings.Contains(localPart, "..")
}

func validDomain(domain string) bool {
	if len(domain) > 253 || !strings.Contains(domain, ".") {
		return false
	}

	labels := strings.Split(domain, ".")
	labelPattern := regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

	for _, label := range labels {
		if !labelPattern.MatchString(label) {
			return false
		}
	}

	return len(labels[len(labels)-1]) >= 2
}

func classifyRole(address string) (string, bool) {
	atIndex := strings.LastIndexByte(address, '@')
	if atIndex <= 0 {
		return "", false
	}

	localPart := strings.SplitN(address[:atIndex], "+", 2)[0]

	tokens := strings.FieldsFunc(localPart, func(character rune) bool {
		return character == '.' || character == '_' || character == '-'
	})
	if len(tokens) == 0 {
		return "", false
	}

	firstToken := tokens[0]
	for _, role := range []string{
		"info",
		"sales",
		"support",
		"contact",
		"admin",
		"owner",
		"billing",
		"careers",
	} {
		if firstToken == role {
			return role, true
		}
	}

	return "", false
}

func personalLooking(address string) bool {
	atIndex := strings.LastIndexByte(address, '@')
	if atIndex <= 0 {
		return false
	}

	localPart := strings.SplitN(address[:atIndex], "+", 2)[0]

	parts := strings.FieldsFunc(localPart, func(character rune) bool {
		return character == '.' || character == '_' || character == '-'
	})
	if len(parts) < 2 {
		return false
	}

	alphaPattern := regexp.MustCompile(`^[a-z]{2,24}$`)

	return alphaPattern.MatchString(parts[0]) && alphaPattern.MatchString(parts[1])
}

func lookupMX(ctx context.Context, domain string, lookup MXLookup) mxResult {
	if lookup == nil {
		lookup = net.DefaultResolver
	}

	records, err := lookup.LookupMX(ctx, domain)
	if err != nil {
		var dnsError *net.DNSError
		if errors.As(err, &dnsError) && dnsError.IsNotFound {
			return mxResult{status: MXMissing}
		}

		return mxResult{status: MXError, err: err.Error()}
	}

	if len(records) == 0 {
		return mxResult{status: MXMissing}
	}

	hosts := make([]string, 0, len(records))

	for _, record := range records {
		if record == nil {
			continue
		}

		host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(record.Host)), ".")
		if host != "" {
			hosts = append(hosts, host)
		}
	}

	sort.Strings(hosts)

	if len(hosts) == 0 {
		return mxResult{status: MXMissing}
	}

	return mxResult{status: MXPresent, records: hosts}
}

func emailRelevance(email *Email, websiteDomain string) int {
	if !email.ValidSyntax {
		return 0
	}

	score := 30
	if sameRegistrableDomain(email.Domain, websiteDomain) {
		score += 20
	}

	roleScores := map[string]int{
		"contact": 16,
		"sales":   15,
		"owner":   14,
		"info":    12,
		"admin":   8,
		"billing": 7,
		"support": 6,
		"careers": 3,
	}

	score += roleScores[email.Role]
	if email.PersonalLikely {
		score += 5
	}

	switch email.MXStatus {
	case MXPresent:
		score += 20
	case MXMissing:
		score -= 20
	case MXNotChecked, MXError:
	}

	bestSourceScore := 0

	for _, source := range email.Sources {
		sourceScore := 0

		switch source.PageKind {
		case PageContact:
			sourceScore += 10
		case PageAbout:
			sourceScore += 6
		case PageHomepage:
			sourceScore += 2
		}

		if source.Method == MethodMailto {
			sourceScore += 3
		}

		if sourceScore > bestSourceScore {
			bestSourceScore = sourceScore
		}
	}

	score += bestSourceScore + minInt(len(email.Sources), 5)
	if email.Disposable {
		score -= 50
	}

	if score < 0 {
		return 0
	}

	if score > 100 {
		return 100
	}

	return score
}

func websiteHost(rawURL string) string {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}

	host := strings.TrimSuffix(strings.ToLower(parsedURL.Hostname()), ".")
	asciiHost, err := idna.Lookup.ToASCII(host)

	if err != nil {
		return host
	}

	return strings.ToLower(asciiHost)
}

func sameRegistrableDomain(left, right string) bool {
	if left == "" || right == "" {
		return false
	}

	if strings.EqualFold(left, right) {
		return true
	}

	leftRegistrable, leftErr := publicsuffix.EffectiveTLDPlusOne(left)
	rightRegistrable, rightErr := publicsuffix.EffectiveTLDPlusOne(right)

	return leftErr == nil && rightErr == nil && strings.EqualFold(leftRegistrable, rightRegistrable)
}

func disposableDomain(domain string, disposableDomains map[string]struct{}) bool {
	for candidate := domain; candidate != ""; {
		if _, found := disposableDomains[candidate]; found {
			return true
		}

		dot := strings.IndexByte(candidate, '.')
		if dot < 0 {
			break
		}

		candidate = candidate[dot+1:]
	}

	return false
}

func defaultDisposableDomains() map[string]struct{} {
	result := make(map[string]struct{})
	for _, domain := range []string{
		"10minutemail.com",
		"dispostable.com",
		"fakeinbox.com",
		"guerrillamail.com",
		"maildrop.cc",
		"mailinator.com",
		"sharklasers.com",
		"temp-mail.org",
		"tempmail.com",
		"throwawaymail.com",
		"trashmail.com",
		"yopmail.com",
	} {
		result[domain] = struct{}{}
	}

	return result
}

func appendUniqueSource(sources []Source, candidate Source) []Source {
	for _, source := range sources {
		if source == candidate {
			return sources
		}
	}

	return append(sources, candidate)
}

func sortSources(sources []Source) {
	sort.Slice(sources, func(left, right int) bool {
		if sources[left].PageURL != sources[right].PageURL {
			return sources[left].PageURL < sources[right].PageURL
		}

		if sources[left].PageKind != sources[right].PageKind {
			return sources[left].PageKind < sources[right].PageKind
		}

		return sources[left].Method < sources[right].Method
	})
}
