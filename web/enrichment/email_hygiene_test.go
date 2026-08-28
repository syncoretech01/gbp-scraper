//nolint:testpackage // Package-internal tests exercise deterministic extraction helpers.
package enrichment

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// TestSanitizeEmailCandidateHandlesLiveWorkspaceJunk replays the exact stored
// values that the acceptance job cfe2d653 wrote into the emails table. Every
// one of them was presented to the operator as a contact for a real business.
func TestSanitizeEmailCandidateHandlesLiveWorkspaceJunk(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		raw      string
		want     string
		repaired bool
		reason   string
	}{
		{
			name: "phone glued in front of the mailbox is trimmed",
			// Stored as a contact for Neptune Tattoo Studio.
			raw:      "626-554-7744inquiries@neptunetattoostudio.com",
			want:     "inquiries@neptunetattoostudio.com",
			repaired: true,
		},
		{
			name:     "sentence punctuation glued in front of the mailbox is trimmed",
			raw:      "shop!estatetattoo@gmail.com",
			want:     "estatetattoo@gmail.com",
			repaired: true,
		},
		{
			name: "page text welded onto the top-level domain is refused",
			// "la@baronart.tattoo" followed by the word "Open".
			raw:    "563-2030la@baronart.tattooopen",
			reason: RejectionUnknownTLD,
		},
		{
			name:   "navigation labels welded onto the top-level domain are refused",
			raw:    "filler@godaddy.combookingsordersmy",
			reason: RejectionUnknownTLD,
		},
		{
			name:   "the GoDaddy template filler mailbox is refused",
			raw:    "filler@godaddy.com",
			reason: RejectionPlaceholder,
		},
		{
			name:   "an asset file name is not a mailbox",
			raw:    "logo@2x.png",
			reason: RejectionAssetPath,
		},
		{
			name:   "a bare token without a domain is refused",
			raw:    "bad-address",
			reason: RejectionSyntax,
		},
		{
			name:   "a local part that is only a phone run cannot be recovered",
			raw:    "6265547744@neptunetattoostudio.com",
			reason: RejectionConcatenated,
		},
		{
			name:     "a real mailbox is untouched",
			raw:      "info@mantletattoo.com",
			want:     "info@mantletattoo.com",
			repaired: false,
		},
		{
			name:     "a real mailbox on a new generic top-level domain is untouched",
			raw:      "la@baronart.tattoo",
			want:     "la@baronart.tattoo",
			repaired: false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			sanitized, reason, ok := sanitizeEmailCandidate(testCase.raw)
			if testCase.want == "" {
				if ok {
					t.Fatalf("sanitizeEmailCandidate(%q) accepted %q, want rejection %q",
						testCase.raw, sanitized.address, testCase.reason)
				}
				if reason != testCase.reason {
					t.Fatalf("sanitizeEmailCandidate(%q) reason = %q, want %q",
						testCase.raw, reason, testCase.reason)
				}

				return
			}

			if !ok {
				t.Fatalf("sanitizeEmailCandidate(%q) rejected with %q, want %q",
					testCase.raw, reason, testCase.want)
			}
			if sanitized.address != testCase.want {
				t.Fatalf("sanitizeEmailCandidate(%q) = %q, want %q",
					testCase.raw, sanitized.address, testCase.want)
			}
			if sanitized.repaired != testCase.repaired {
				t.Fatalf("sanitizeEmailCandidate(%q) repaired = %v, want %v",
					testCase.raw, sanitized.repaired, testCase.repaired)
			}
		})
	}
}

// TestExtractEmailsSeparatesNeighbouringElements proves the concatenation
// defect at its source: goquery's Text() glues neighbouring elements, and the
// crawler used to scan that glued run.
func TestExtractEmailsSeparatesNeighbouringElements(t *testing.T) {
	t.Parallel()

	page := `<html><body>
		<div class="contact">
			<a href="tel:6265547744">626-554-7744</a><a href="/x">inquiries@neptunetattoostudio.com</a>
		</div>
		<footer><span>la@baronart.tattoo</span><span>Open</span></footer>
		<nav><span>filler@godaddy.com</span><a>Bookings</a><a>Orders</a></nav>
	</body></html>`

	extracted, err := extractPage([]byte(page), "https://example.com/", PageHomepage)
	if err != nil {
		t.Fatalf("extractPage() error = %v", err)
	}

	found := make([]string, 0, len(extracted.emails))
	for _, email := range extracted.emails {
		found = append(found, email.address)
	}

	for _, glued := range []string{
		"626-554-7744inquiries@neptunetattoostudio.com",
		"la@baronart.tattooopen",
		"filler@godaddy.combookingsorders",
	} {
		for _, candidate := range found {
			if strings.EqualFold(candidate, glued) {
				t.Fatalf("element boundaries were not separated: found %q in %v", glued, found)
			}
		}
	}

	for _, want := range []string{
		"inquiries@neptunetattoostudio.com",
		"la@baronart.tattoo",
	} {
		if !containsString(found, want) {
			t.Fatalf("expected %q among extracted candidates %v", want, found)
		}
	}
}

// TestAnalyzeRawEmailsReportsFunnel proves that a crawl which finds candidates
// but exports nothing can always explain itself.
func TestAnalyzeRawEmailsReportsFunnel(t *testing.T) {
	t.Parallel()

	source := Source{PageURL: "https://neptunetattoostudio.com/", PageKind: PageHomepage, Method: MethodVisibleText}
	findings := []rawEmail{
		{address: "626-554-7744inquiries@neptunetattoostudio.com", source: source},
		{address: "626-554-7744inquiries@neptunetattoostudio.com", source: source},
		{address: "la@baronart.tattooopen", source: source},
		{address: "filler@godaddy.com", source: source},
		{address: "logo@2x.png", source: source},
		{address: "bad-address", source: source},
	}

	emails, funnel, err := analyzeRawEmails(context.Background(), findings, EmailAnalysisConfig{
		WebsiteURL: "https://neptunetattoostudio.com/",
	})
	if err != nil {
		t.Fatalf("analyzeRawEmails() error = %v", err)
	}

	if len(emails) != 1 || emails[0].Address != "inquiries@neptunetattoostudio.com" {
		t.Fatalf("accepted emails = %#v", emails)
	}

	want := EmailFunnel{
		Discovered: 6,
		Distinct:   5,
		Accepted:   1,
		Rejected:   4,
		Repaired:   1,
		RejectionReasons: map[string]int{
			RejectionUnknownTLD:  1,
			RejectionPlaceholder: 1,
			RejectionAssetPath:   1,
			RejectionSyntax:      1,
		},
	}

	if !reflect.DeepEqual(funnel, want) {
		t.Fatalf("funnel = %#v, want %#v", funnel, want)
	}

	if reasons := funnel.Reasons(); !reflect.DeepEqual(reasons, []string{
		RejectionAssetPath, RejectionPlaceholder, RejectionSyntax, RejectionUnknownTLD,
	}) {
		t.Fatalf("funnel reasons = %v", reasons)
	}
}
