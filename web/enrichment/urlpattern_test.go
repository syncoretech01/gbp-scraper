//nolint:testpackage // Package-internal tests cover the unexported glob matcher.
package enrichment

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestMatchURLPatternImplementsTheDocumentedGlobGrammar(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		{name: "literal path matches itself", pattern: "/contact", path: "/contact", want: true},
		{name: "literal path does not match a prefix", pattern: "/contact", path: "/contacts", want: false},
		{name: "unanchored pattern is anchored to the root", pattern: "contact", path: "/contact", want: true},
		{name: "trailing star matches a suffix", pattern: "/contact*", path: "/contact-us", want: true},
		{name: "trailing star matches the empty suffix", pattern: "/contact*", path: "/contact", want: true},
		{name: "star spans separators", pattern: "/blog/*", path: "/blog/2024/06/post", want: true},
		{name: "star does not match a missing separator", pattern: "/blog/*", path: "/blog", want: false},
		{name: "leading star is not re-anchored", pattern: "*/cart*", path: "/shop/cart/checkout", want: true},
		{name: "question mark matches exactly one character", pattern: "/a?c", path: "/abc", want: true},
		{name: "question mark does not match two characters", pattern: "/a?c", path: "/abbc", want: false},
		{name: "question mark does not match zero characters", pattern: "/a?c", path: "/ac", want: false},
		{name: "bare star matches everything", pattern: "*", path: "/anything/at/all", want: true},
		{name: "root pattern matches the root path", pattern: "/", path: "/", want: true},
		{name: "multiple stars still match", pattern: "/*/team/*", path: "/en/team/leadership", want: true},
		{name: "non-matching literal fails", pattern: "/about", path: "/contact", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := matchURLPattern(test.pattern, test.path); got != test.want {
				t.Fatalf("matchURLPattern(%q, %q) = %t, want %t", test.pattern, test.path, got, test.want)
			}
		})
	}
}

// TestMatchURLPatternTerminatesOnAdversarialInput is the whole reason the
// matcher is a glob and not a regular expression. The classic catastrophic
// backtracking shape is linear here, so a hostile pattern cannot hang a crawl.
func TestMatchURLPatternTerminatesOnAdversarialInput(t *testing.T) {
	t.Parallel()

	pattern := "/" + strings.Repeat("*a", 60) + "*"
	path := "/" + strings.Repeat("a", 190)

	done := make(chan bool, 1)

	go func() { done <- matchURLPattern(pattern, path) }()

	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal("matchURLPattern did not terminate on an adversarial pattern")
	}
}

func TestNormalizeURLPatternsCanonicalisesAndBounds(t *testing.T) {
	t.Parallel()

	normalized, err := NormalizeURLPatterns([]string{"  /Contact*  ", "/contact*", "", "   ", "/About"})
	if err != nil {
		t.Fatalf("NormalizeURLPatterns() error = %v", err)
	}

	want := []string{"/contact*", "/about"}
	if len(normalized) != len(want) {
		t.Fatalf("NormalizeURLPatterns() = %v, want %v", normalized, want)
	}

	for index, pattern := range want {
		if normalized[index] != pattern {
			t.Fatalf("NormalizeURLPatterns()[%d] = %q, want %q", index, normalized[index], pattern)
		}
	}

	if empty, err := NormalizeURLPatterns([]string{"", "  ", "\t"}); err != nil || empty != nil {
		t.Fatalf("NormalizeURLPatterns(blank) = %v, %v, want nil, nil", empty, err)
	}

	tooMany := make([]string, 0, MaximumURLPatterns+1)
	for index := range MaximumURLPatterns + 1 {
		tooMany = append(tooMany, fmt.Sprintf("/page-%d", index))
	}

	if _, err := NormalizeURLPatterns(tooMany); !errors.Is(err, ErrInvalidURLPattern) {
		t.Fatalf("NormalizeURLPatterns(too many) error = %v, want ErrInvalidURLPattern", err)
	}

	long := "/" + strings.Repeat("a", MaximumURLPatternLength)
	if _, err := NormalizeURLPatterns([]string{long}); !errors.Is(err, ErrInvalidURLPattern) {
		t.Fatalf("NormalizeURLPatterns(too long) error = %v, want ErrInvalidURLPattern", err)
	}

	if _, err := NormalizeURLPatterns([]string{"/a b"}); !errors.Is(err, ErrInvalidURLPattern) {
		t.Fatalf("NormalizeURLPatterns(whitespace) error = %v, want ErrInvalidURLPattern", err)
	}
}

func TestURLPatternSetNormalizedBoundsBothListsIndependently(t *testing.T) {
	t.Parallel()

	include := make([]string, 0, MaximumURLPatterns)
	exclude := make([]string, 0, MaximumURLPatterns)

	for index := range MaximumURLPatterns {
		include = append(include, fmt.Sprintf("/include-%d", index))
		exclude = append(exclude, fmt.Sprintf("/exclude-%d", index))
	}

	set, err := URLPatternSet{Include: include, Exclude: exclude}.Normalized()
	if err != nil {
		t.Fatalf("Normalized() error = %v", err)
	}

	if len(set.Include) != MaximumURLPatterns || len(set.Exclude) != MaximumURLPatterns {
		t.Fatalf("Normalized() kept %d include and %d exclude patterns, want %d of each",
			len(set.Include), len(set.Exclude), MaximumURLPatterns)
	}

	if _, err := (URLPatternSet{Exclude: append(exclude, "/one-too-many")}).Normalized(); err == nil {
		t.Fatal("Normalized() accepted an over-long exclude list")
	}
}

func TestURLPatternSetAllowsGivesExcludesPrecedence(t *testing.T) {
	t.Parallel()

	empty := URLPatternSet{}
	if !empty.Empty() {
		t.Fatal("the zero set must report itself as empty")
	}

	// An empty set is the pre-existing behaviour: it filters nothing at all.
	for _, candidate := range []string{"https://example.com/", "https://example.com/blog/post", "not a url"} {
		if !empty.Allows(candidate) {
			t.Fatalf("the empty set refused %q; it must filter nothing", candidate)
		}
	}

	includeOnly := URLPatternSet{Include: []string{"/contact*", "/about"}}
	if !includeOnly.Allows("https://example.com/contact-us") {
		t.Fatal("an included path must be allowed")
	}

	if includeOnly.Allows("https://example.com/blog/post") {
		t.Fatal("an include list must act as an allow-list")
	}

	excludeOnly := URLPatternSet{Exclude: []string{"/blog/*"}}
	if !excludeOnly.Allows("https://example.com/contact") {
		t.Fatal("without an include list anything not excluded must be allowed")
	}

	if excludeOnly.Allows("https://example.com/blog/post") {
		t.Fatal("an excluded path must be refused")
	}

	// Exclude beats include even when both name the same path.
	both := URLPatternSet{Include: []string{"/contact*"}, Exclude: []string{"/contact/private*"}}
	if !both.Allows("https://example.com/contact-us") {
		t.Fatal("an included path outside the exclusion must still be allowed")
	}

	if both.Allows("https://example.com/contact/private/notes") {
		t.Fatal("exclude must take precedence over include")
	}

	// A filtering set refuses what it cannot parse, because an unparsable URL
	// cannot be shown to satisfy the operator's rules.
	if both.Allows("https://example.com/\x7f") {
		t.Fatal("a filtering set must refuse an unparsable candidate")
	}

	// Matching is case-insensitive over the path.
	if !includeOnly.Allows("https://example.com/CONTACT-US") {
		t.Fatal("pattern matching must be case-insensitive")
	}

	// Query strings and fragments are never matched.
	if excludeOnly.Allows("https://example.com/blog/post?utm=1#top") {
		t.Fatal("the path must be matched with the query and fragment ignored")
	}
}

func TestURLPatternSetEvidenceRecordsWhatWasApplied(t *testing.T) {
	t.Parallel()

	if evidence := (URLPatternSet{}).Evidence([]string{"https://example.com/blog"}); evidence != nil {
		t.Fatalf("an empty set must record no evidence, got %+v", evidence)
	}

	set := URLPatternSet{Include: []string{"/contact*"}, Exclude: []string{"/blog/*"}}

	skipped := make([]string, 0, maximumRecordedPatternSkips*2)
	for index := range maximumRecordedPatternSkips * 2 {
		skipped = append(skipped, fmt.Sprintf("https://example.com/blog/%03d", index))
	}

	// Duplicates and blanks must not inflate the count.
	skipped = append(skipped, skipped[0], "  ")

	evidence := set.Evidence(skipped)
	if evidence == nil {
		t.Fatal("a filtering set must record evidence")
	}

	if len(evidence.Include) != 1 || evidence.Include[0] != "/contact*" {
		t.Fatalf("evidence include = %v, want the configured include list", evidence.Include)
	}

	if len(evidence.Exclude) != 1 || evidence.Exclude[0] != "/blog/*" {
		t.Fatalf("evidence exclude = %v, want the configured exclude list", evidence.Exclude)
	}

	if evidence.SkippedCount != maximumRecordedPatternSkips*2 {
		t.Fatalf("evidence skipped count = %d, want %d", evidence.SkippedCount, maximumRecordedPatternSkips*2)
	}

	if len(evidence.SkippedURLs) != maximumRecordedPatternSkips {
		t.Fatalf("evidence recorded %d URLs, want the list bounded at %d",
			len(evidence.SkippedURLs), maximumRecordedPatternSkips)
	}

	for index := 1; index < len(evidence.SkippedURLs); index++ {
		if evidence.SkippedURLs[index-1] >= evidence.SkippedURLs[index] {
			t.Fatalf("evidence URLs are not sorted: %v", evidence.SkippedURLs)
		}
	}
}
