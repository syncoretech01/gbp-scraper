package enrichment

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// URL pattern bounds. The limits are deliberately small: patterns are an
// operator convenience for steering one bounded crawl, not a routing language,
// and a small ceiling keeps both the stored evidence and the per-candidate
// matching cost trivially bounded.
const (
	// MaximumURLPatterns is how many include or exclude patterns one crawl
	// may carry. The two lists are bounded independently.
	MaximumURLPatterns = 20
	// MaximumURLPatternLength is the longest single pattern, in bytes.
	MaximumURLPatternLength = 200
	// maximumRecordedPatternSkips bounds how many skipped URLs one audit
	// records as evidence, so a large site cannot inflate the stored result.
	maximumRecordedPatternSkips = 25
)

// ErrInvalidURLPattern identifies a crawl URL pattern that breaks the bounds
// above or contains characters a URL path can never hold.
var ErrInvalidURLPattern = errors.New("invalid crawl URL pattern")

// ErrURLPatternExcluded reports that a URL was not fetched because the
// configured crawl URL patterns excluded it.
var ErrURLPatternExcluded = errors.New("excluded by the configured crawl URL patterns")

// URLPatternSet is the operator's control over which same-origin pages one
// bounded audit may visit beyond the entry page.
//
// Patterns are simple globs matched against the candidate URL's path. They are
// deliberately not regular expressions: an operator-supplied regexp can be
// pathological, and nothing here is worth the risk of a crawl that never
// finishes. The whole grammar is:
//
//   - matches any run of characters, including "/"
//     ?  matches exactly one character
//
// Every other character is literal. Matching is case-insensitive over the
// escaped path, the whole path must match the whole pattern, and a pattern
// that begins with neither "/" nor "*" is anchored to the path root, so
// "contact*" and "/contact*" are the same pattern. Query strings and fragments
// are never matched.
//
// Exclude always beats include. Both lists empty means "no filtering", which
// is exactly the behaviour every crawl had before patterns existed.
type URLPatternSet struct {
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

// URLPatternEvidence is the operator-visible record of what one crawl's URL
// patterns were and what they actually kept out. It is written into the
// immutable audit result so an operator can see which rules were in force for
// a run rather than only which rules are configured now.
type URLPatternEvidence struct {
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
	// SkippedURLs are the candidate same-origin URLs the patterns kept out,
	// bounded and sorted. SkippedCount is the untruncated total.
	SkippedURLs  []string `json:"skipped_urls,omitempty"`
	SkippedCount int      `json:"skipped_count,omitempty"`
}

// NormalizeURLPatterns validates and canonicalises one list of crawl URL
// patterns: surrounding space is trimmed, empty entries are dropped, entries
// are lowercased, duplicates are removed, and order is preserved. It returns
// nil for a list that held nothing usable, so an all-blank textarea is
// indistinguishable from an unset one.
func NormalizeURLPatterns(patterns []string) ([]string, error) {
	if len(patterns) == 0 {
		return nil, nil
	}

	normalized := make([]string, 0, len(patterns))
	seen := make(map[string]struct{}, len(patterns))

	for _, pattern := range patterns {
		candidate := strings.ToLower(strings.TrimSpace(pattern))
		if candidate == "" {
			continue
		}

		if len(candidate) > MaximumURLPatternLength {
			return nil, fmt.Errorf(
				"%w: a pattern may be at most %d characters", ErrInvalidURLPattern, MaximumURLPatternLength,
			)
		}

		if strings.ContainsAny(candidate, " \t\r\n") {
			return nil, fmt.Errorf("%w: %q contains whitespace", ErrInvalidURLPattern, pattern)
		}

		if _, duplicate := seen[candidate]; duplicate {
			continue
		}

		seen[candidate] = struct{}{}

		normalized = append(normalized, candidate)
	}

	if len(normalized) == 0 {
		return nil, nil
	}

	if len(normalized) > MaximumURLPatterns {
		return nil, fmt.Errorf(
			"%w: at most %d patterns are allowed, got %d", ErrInvalidURLPattern, MaximumURLPatterns, len(normalized),
		)
	}

	return normalized, nil
}

// Normalized returns a validated copy of the set. Both lists are bounded
// independently, so an operator can supply twenty includes and twenty
// excludes.
func (set URLPatternSet) Normalized() (URLPatternSet, error) {
	include, err := NormalizeURLPatterns(set.Include)
	if err != nil {
		return URLPatternSet{}, fmt.Errorf("include patterns: %w", err)
	}

	exclude, err := NormalizeURLPatterns(set.Exclude)
	if err != nil {
		return URLPatternSet{}, fmt.Errorf("exclude patterns: %w", err)
	}

	return URLPatternSet{Include: include, Exclude: exclude}, nil
}

// Empty reports whether the set filters nothing. An empty set keeps exactly
// the crawler's historical page-selection behaviour.
func (set URLPatternSet) Empty() bool {
	return len(set.Include) == 0 && len(set.Exclude) == 0
}

// Allows reports whether a candidate URL may be fetched. Excludes are checked
// first and win outright; an include list then acts as an allow-list, and no
// include list means "everything not excluded".
//
// A candidate that cannot be parsed is refused whenever the set filters
// anything, because an unparsable URL cannot be shown to satisfy the
// operator's rules.
func (set URLPatternSet) Allows(rawURL string) bool {
	if set.Empty() {
		return true
	}

	path, ok := patternPath(rawURL)
	if !ok {
		return false
	}

	for _, pattern := range set.Exclude {
		if matchURLPattern(pattern, path) {
			return false
		}
	}

	if len(set.Include) == 0 {
		return true
	}

	for _, pattern := range set.Include {
		if matchURLPattern(pattern, path) {
			return true
		}
	}

	return false
}

// Evidence renders the operator-visible record of this set and the candidate
// URLs it kept out of one crawl. It returns nil for a set that filters
// nothing, so an audit run without patterns stores exactly what it always did.
func (set URLPatternSet) Evidence(skipped []string) *URLPatternEvidence {
	if set.Empty() {
		return nil
	}

	evidence := URLPatternEvidence{
		Include: append([]string(nil), set.Include...),
		Exclude: append([]string(nil), set.Exclude...),
	}

	unique := make(map[string]struct{}, len(skipped))

	for _, candidate := range skipped {
		if strings.TrimSpace(candidate) == "" {
			continue
		}

		unique[candidate] = struct{}{}
	}

	// sortedKeys already orders the set, so truncation keeps a stable,
	// reproducible prefix rather than an arbitrary sample.
	evidence.SkippedCount = len(unique)
	evidence.SkippedURLs = sortedKeys(unique)

	if len(evidence.SkippedURLs) > maximumRecordedPatternSkips {
		evidence.SkippedURLs = evidence.SkippedURLs[:maximumRecordedPatternSkips]
	}

	return &evidence
}

// patternPath extracts the lowercased, root-anchored path a pattern is matched
// against. It reports false when the URL cannot be parsed.
func patternPath(rawURL string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", false
	}

	path := strings.ToLower(parsed.EscapedPath())
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	return path, true
}

// matchURLPattern reports whether one glob pattern matches one path in full.
//
// The implementation is the classic iterative two-pointer glob match with a
// single backtrack point. It allocates nothing, never recurses, and visits
// each byte of the path at most twice per star, so the worst case is bounded
// by the pattern and path lengths rather than by pattern structure. That is
// the whole reason this is a glob and not a regular expression.
func matchURLPattern(pattern, path string) bool {
	if !strings.HasPrefix(pattern, "/") && !strings.HasPrefix(pattern, "*") {
		pattern = "/" + pattern
	}

	patternIndex, pathIndex := 0, 0
	starIndex, matchIndex := -1, 0

	for pathIndex < len(path) {
		switch {
		case patternIndex < len(pattern) &&
			(pattern[patternIndex] == '?' || pattern[patternIndex] == path[pathIndex]):
			patternIndex++
			pathIndex++
		case patternIndex < len(pattern) && pattern[patternIndex] == '*':
			starIndex = patternIndex
			matchIndex = pathIndex
			patternIndex++
		case starIndex >= 0:
			patternIndex = starIndex + 1
			matchIndex++
			pathIndex = matchIndex
		default:
			return false
		}
	}

	for patternIndex < len(pattern) && pattern[patternIndex] == '*' {
		patternIndex++
	}

	return patternIndex == len(pattern)
}
