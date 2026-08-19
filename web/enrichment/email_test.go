//nolint:testpackage // Package-internal tests exercise deterministic extraction helpers.
package enrichment

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"
)

func TestAnalyzeEmailsClassifiesChecksAndRanksDeterministically(t *testing.T) {
	t.Parallel()

	calls := make(map[string]int)
	lookup := MXLookupFunc(func(_ context.Context, domain string) ([]*net.MX, error) {
		calls[domain]++

		if domain == "missing.example" {
			return nil, &net.DNSError{IsNotFound: true}
		}

		return []*net.MX{
			{Host: "mx2." + domain + ".", Pref: 20},
			{Host: "mx1." + domain + ".", Pref: 10},
		}, nil
	})
	homepageMailto := Source{
		PageURL:  "https://www.example.com/",
		PageKind: PageHomepage,
		Method:   MethodMailto,
	}
	contactText := Source{
		PageURL:  "https://www.example.com/contact",
		PageKind: PageContact,
		Method:   MethodVisibleText,
	}
	candidates := []EmailCandidate{
		{Address: "Info@Example.COM", Source: homepageMailto},
		{Address: "info@example.com", Source: contactText},
		{Address: "jane.doe@example.com", Source: contactText},
		{Address: "throw@mailinator.com", Source: homepageMailto},
		{Address: "bad-address", Source: homepageMailto},
		{Address: "lost@missing.example", Source: homepageMailto},
	}
	config := EmailAnalysisConfig{
		WebsiteURL: "https://www.example.com",
		CheckMX:    true,
		MXLookup:   lookup,
	}

	first, err := AnalyzeEmails(context.Background(), candidates, config)
	if err != nil {
		t.Fatalf("AnalyzeEmails() error = %v", err)
	}

	second, err := AnalyzeEmails(context.Background(), candidates, config)
	if err != nil {
		t.Fatalf("second AnalyzeEmails() error = %v", err)
	}

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("analysis is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
	}

	info := emailByAddress(t, first, "info@example.com")
	if !info.ValidSyntax || !info.RoleAddress || info.Role != "info" {
		t.Fatalf("role classification = %#v", info)
	}

	if info.MXStatus != MXPresent || !reflect.DeepEqual(info.MXRecords, []string{"mx1.example.com", "mx2.example.com"}) {
		t.Fatalf("MX result = %#v", info)
	}

	if len(info.Sources) != 2 {
		t.Fatalf("info sources = %d, want 2", len(info.Sources))
	}

	personal := emailByAddress(t, first, "jane.doe@example.com")
	if !personal.PersonalLikely || personal.RoleAddress {
		t.Fatalf("personal classification = %#v", personal)
	}

	disposable := emailByAddress(t, first, "throw@mailinator.com")
	if !disposable.Disposable {
		t.Fatalf("disposable classification = %#v", disposable)
	}

	missing := emailByAddress(t, first, "lost@missing.example")
	if missing.MXStatus != MXMissing {
		t.Fatalf("missing MX status = %q", missing.MXStatus)
	}

	invalid := emailByAddress(t, first, "bad-address")
	if invalid.ValidSyntax || invalid.Relevance != 0 || invalid.Rank != len(first) {
		t.Fatalf("invalid address result = %#v", invalid)
	}

	for index, email := range first {
		if email.Rank != index+1 {
			t.Fatalf("rank at index %d = %d", index, email.Rank)
		}
	}

	if calls["example.com"] != 2 {
		// Two complete analyses were run; each must cache duplicate domains once.
		t.Fatalf("example.com MX calls = %d, want 2", calls["example.com"])
	}
}

func TestAnalyzeEmailsCustomDisposableAndCancellation(t *testing.T) {
	t.Parallel()

	source := Source{PageURL: "https://example.test", PageKind: PageHomepage, Method: MethodVisibleText}

	emails, err := AnalyzeEmails(context.Background(), []EmailCandidate{
		{Address: "hello@temporary.example", Source: source},
	}, EmailAnalysisConfig{DisposableDomains: []string{"temporary.example"}})
	if err != nil {
		t.Fatalf("AnalyzeEmails() error = %v", err)
	}

	if len(emails) != 1 || !emails[0].Disposable || emails[0].MXStatus != MXNotChecked {
		t.Fatalf("custom disposable result = %#v", emails)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = AnalyzeEmails(ctx, []EmailCandidate{{Address: "info@example.com", Source: source}}, EmailAnalysisConfig{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled analysis error = %v, want context.Canceled", err)
	}
}

func emailByAddress(t *testing.T, emails []Email, address string) Email {
	t.Helper()

	for index := range emails {
		email := emails[index]
		if email.Address == address {
			return email
		}
	}

	t.Fatalf("email %q not found in %#v", address, emails)

	return Email{}
}
