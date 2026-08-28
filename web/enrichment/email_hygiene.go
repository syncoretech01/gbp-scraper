package enrichment

import (
	"regexp"
	"sort"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// EmailRejection names why one extracted candidate never became a stored
// address. The values are stable strings so a monitor, an export, or a test
// can group by them without re-deriving the rule.
const (
	// RejectionSyntax covers a candidate that is not a syntactically usable
	// address at all: no single "@", an empty local part, or a domain the
	// label rules reject.
	RejectionSyntax = "syntax"
	// RejectionUnknownTLD covers a domain whose last label is not an ICANN
	// top-level domain. This is what catches text-run damage that ends in a
	// real domain, such as "baronart.tattooopen".
	RejectionUnknownTLD = "unknown_tld"
	// RejectionAssetPath covers a "domain" that is really a file name, such as
	// logo@2x.png or an inline sprite reference.
	RejectionAssetPath = "asset_path"
	// RejectionPlaceholder covers vendor and template filler addresses that are
	// present on the page but belong to nobody, such as GoDaddy's
	// filler@godaddy.com or an error-reporting DSN.
	RejectionPlaceholder = "placeholder"
	// RejectionConcatenated covers a candidate whose local part is a run of
	// glued page text that no bounded repair could recover.
	RejectionConcatenated = "concatenated_text"
	// RejectionLocalPartLength covers a local part longer than any real
	// mailbox, which in practice only happens after text-run damage.
	RejectionLocalPartLength = "local_part_too_long"
)

// maximumSaneLocalPart bounds the local part accepted from page text. RFC 5321
// allows 64 octets; addresses that long do not occur on business websites and
// in this crawler they have always been glued page text.
const maximumSaneLocalPart = 40

// EmailFunnel is the honest accounting for one crawl's email extraction: how
// many candidate occurrences the pages produced, how many distinct addresses
// that was, how many survived hygiene, and why the rest did not.
//
// It exists because a run that reports "emails discovered" while exporting
// none is indistinguishable from a broken run. Every rejected candidate is
// counted under a named reason, so the difference is always explainable.
type EmailFunnel struct {
	// Discovered counts raw candidate occurrences across every crawled page,
	// including repeats of the same address.
	Discovered int `json:"discovered"`
	// Distinct counts the candidates after collapsing repeats.
	Distinct int `json:"distinct"`
	// Accepted counts the addresses that reached the result.
	Accepted int `json:"accepted"`
	// Rejected counts the distinct candidates hygiene refused.
	Rejected int `json:"rejected"`
	// Repaired counts accepted addresses that needed a deterministic repair,
	// such as trimming a phone number glued onto the local part.
	Repaired int `json:"repaired"`
	// RejectionReasons maps a rejection constant to how many distinct
	// candidates it removed.
	RejectionReasons map[string]int `json:"rejection_reasons,omitempty"`
}

// Reasons returns the rejection reasons in a deterministic order so a report
// or a test never depends on Go's map iteration order.
func (funnel EmailFunnel) Reasons() []string {
	reasons := make([]string, 0, len(funnel.RejectionReasons))
	for reason := range funnel.RejectionReasons {
		reasons = append(reasons, reason)
	}

	sort.Strings(reasons)

	return reasons
}

func (funnel *EmailFunnel) reject(reason string) {
	if funnel.RejectionReasons == nil {
		funnel.RejectionReasons = make(map[string]int, 4)
	}

	funnel.Rejected++
	funnel.RejectionReasons[reason]++
}

var (
	// phonePrefixPattern matches a leading run of digits and phone punctuation
	// that page text glued onto a local part, for example the "626-554-7744"
	// in "626-554-7744inquiries@example.com".
	phonePrefixPattern = regexp.MustCompile(`^[0-9][0-9.()+-]{5,}`)
	// unusualLocalPartPattern matches the RFC-legal but practically unused
	// local-part characters. Real mailboxes use letters, digits, dot,
	// underscore, hyphen, and plus; anything else on a business page marks a
	// glued sentence, such as "shop!estatetattoo@gmail.com".
	unusualLocalPartPattern = regexp.MustCompile("[!#$%&'*/=?^`{|}~]")
	// digitRunPattern finds a phone-length digit run anywhere in a local part.
	digitRunPattern = regexp.MustCompile(`[0-9]{7,}`)
)

// assetExtensions are final labels that mean the "domain" is a file name.
var assetExtensions = map[string]struct{}{
	"png": {}, "jpg": {}, "jpeg": {}, "gif": {}, "webp": {}, "svg": {},
	"css": {}, "js": {}, "ico": {}, "woff": {}, "woff2": {}, "ttf": {},
	"eot": {}, "mp4": {}, "webm": {}, "pdf": {}, "json": {}, "xml": {},
	"html": {}, "htm": {}, "php": {}, "avif": {},
}

// placeholderDomains carry vendor infrastructure mailboxes that appear
// verbatim in shipped page source and never belong to the audited business.
// Reserved documentation domains are deliberately absent: they are harmless
// here, and refusing them would reject legitimate local test fixtures.
var placeholderDomains = map[string]struct{}{
	"sentry.io":                {},
	"wixpress.com":             {},
	"sentry.wixpress.com":      {},
	"sentry-next.wixpress.com": {},
}

// placeholderAddresses are exact template filler mailboxes. GoDaddy's website
// builder ships filler@godaddy.com inside its default contact block, and it
// was stored as a real contact for a live business before this rule existed.
var placeholderAddresses = map[string]struct{}{
	"filler@godaddy.com": {},
}

// sanitizedEmail is the outcome of hygiene for one candidate.
type sanitizedEmail struct {
	address  string
	repaired bool
}

// sanitizeEmailCandidate applies deterministic hygiene to one extracted
// candidate. It either returns a usable address (optionally after a bounded
// repair) or the reason the candidate was refused.
//
// The repairs are strictly subtractive: they only ever remove text that a page
// glued in front of a real mailbox. Nothing is invented, and a candidate that
// cannot be recovered is refused rather than guessed at.
func sanitizeEmailCandidate(raw string) (sanitizedEmail, string, bool) {
	trimmed := strings.TrimSpace(strings.Trim(raw, "<>\"'.,;:"))
	if trimmed == "" || strings.Count(trimmed, "@") != 1 {
		return sanitizedEmail{}, RejectionSyntax, false
	}

	parts := strings.SplitN(trimmed, "@", 2)
	localPart := strings.TrimSpace(parts[0])
	domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(parts[1]), "."))

	if localPart == "" || domain == "" {
		return sanitizedEmail{}, RejectionSyntax, false
	}

	if reason, ok := rejectDomain(domain); !ok {
		return sanitizedEmail{}, reason, false
	}

	repairedLocal, repaired, ok := repairLocalPart(localPart)
	if !ok {
		return sanitizedEmail{}, RejectionConcatenated, false
	}

	if len(repairedLocal) > maximumSaneLocalPart {
		return sanitizedEmail{}, RejectionLocalPartLength, false
	}

	address := strings.ToLower(repairedLocal) + "@" + domain
	if _, found := placeholderAddresses[address]; found {
		return sanitizedEmail{}, RejectionPlaceholder, false
	}

	return sanitizedEmail{address: address, repaired: repaired}, "", true
}

// rejectDomain refuses domains that cannot belong to a real mailbox: file
// names, template or vendor infrastructure, and labels whose top level is not
// an ICANN top-level domain. The ICANN test is what catches a domain with page
// text welded onto its end, because "tattooopen" is not a TLD and "tattoo" is.
func rejectDomain(domain string) (string, bool) {
	if !strings.Contains(domain, ".") {
		return RejectionSyntax, false
	}

	labels := strings.Split(domain, ".")
	last := labels[len(labels)-1]

	if _, found := assetExtensions[last]; found {
		return RejectionAssetPath, false
	}

	for candidate := domain; candidate != ""; {
		if _, found := placeholderDomains[candidate]; found {
			return RejectionPlaceholder, false
		}

		dot := strings.IndexByte(candidate, '.')
		if dot < 0 {
			break
		}

		candidate = candidate[dot+1:]
	}

	if _, icann := publicsuffix.PublicSuffix(domain); !icann {
		return RejectionUnknownTLD, false
	}

	return "", true
}

// repairLocalPart removes page text glued in front of a mailbox name. It
// reports the recovered local part, whether a repair was needed, and whether a
// usable local part remains at all.
func repairLocalPart(localPart string) (string, bool, bool) {
	repaired := false

	// A separator character that real mailboxes do not use marks the boundary
	// between glued sentence text and the mailbox, as in "shop!estatetattoo".
	// Keep only the text after the last such character.
	if index := lastMatchIndex(localPart, unusualLocalPartPattern); index >= 0 {
		localPart = localPart[index+1:]
		repaired = true
	}

	// A leading phone number is the other common glue, as in
	// "626-554-7744inquiries". Trim it when a mailbox name survives.
	if match := phonePrefixPattern.FindString(localPart); match != "" {
		remainder := strings.TrimLeft(localPart[len(match):], ".-_")
		if remainder != "" {
			localPart = remainder
			repaired = true
		}
	}

	localPart = strings.Trim(localPart, ".-_")
	if localPart == "" {
		return "", repaired, false
	}

	// Anything still carrying a phone-length digit run is glued text that no
	// bounded rule can split safely.
	if digitRunPattern.MatchString(localPart) {
		return "", repaired, false
	}

	return localPart, repaired, true
}

// lastMatchIndex reports the start index of the final match of pattern in
// value, or -1 when the pattern never matches.
func lastMatchIndex(value string, pattern *regexp.Regexp) int {
	matches := pattern.FindAllStringIndex(value, -1)
	if len(matches) == 0 {
		return -1
	}

	return matches[len(matches)-1][0]
}

// StoredEmailVerdict is the current hygiene ruling on an address that is
// already in the workspace.
type StoredEmailVerdict struct {
	// Address is the value the rules would keep, after any repair.
	Address string
	// Rejected reports that no usable address could be recovered.
	Rejected bool
	// Repaired reports that the stored value carried page text that the rules
	// would now trim away.
	Repaired bool
	// Reason names the rejection when Rejected is set.
	Reason string
}

// ClassifyStoredEmail applies the current hygiene rules to a value that is
// already stored, so an operator can be told how many existing rows a
// re-audit would change and why. It never mutates anything.
func ClassifyStoredEmail(value string) StoredEmailVerdict {
	sanitized, reason, ok := sanitizeEmailCandidate(value)
	if !ok {
		return StoredEmailVerdict{Rejected: true, Reason: reason}
	}

	return StoredEmailVerdict{
		Address:  sanitized.address,
		Repaired: sanitized.repaired || !strings.EqualFold(sanitized.address, strings.TrimSpace(value)),
	}
}
