package resultimport

import "testing"

func TestMismatchedEmailDomainsAreFlaggedButKept(t *testing.T) {
	t.Parallel()

	headers := []string{"title", "website", "emails"}

	tests := []struct {
		name    string
		website string
		emails  string
		want    bool
	}{
		{
			name:    "matching domain is not a mismatch",
			website: "https://bakery.example",
			emails:  "hello@bakery.example",
			want:    false,
		},
		{
			name:    "mail sub-domain still belongs to the site",
			website: "https://bakery.example",
			emails:  "hello@mail.bakery.example",
			want:    false,
		},
		{
			name:    "www prefix on the website still matches",
			website: "https://www.bakery.example",
			emails:  "hello@bakery.example",
			want:    false,
		},
		{
			name:    "a consumer mailbox is expected and not flagged",
			website: "https://bakery.example",
			emails:  "bakery.owner@gmail.com",
			want:    false,
		},
		{
			name:    "an unrelated business domain is flagged",
			website: "https://bakery.example",
			emails:  "sales@some-other-company.example",
			want:    true,
		},
		{
			name:    "one matching address clears the whole row",
			website: "https://bakery.example",
			emails:  "sales@some-other-company.example;hello@bakery.example",
			want:    false,
		},
		{
			name:    "no website means nothing to compare against",
			website: "",
			emails:  "sales@some-other-company.example",
			want:    false,
		},
		{
			name:    "no email means nothing to compare",
			website: "https://bakery.example",
			emails:  "",
			want:    false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			record := mustReadOne(t, makeCSV(t, headers, []string{"Bakery", test.website, test.emails}), Options{})

			found := false
			for _, item := range record.Warnings {
				if item.Code == IssueDomainMismatch {
					found = true
				}
			}

			if found != test.want {
				t.Fatalf("domain mismatch warning = %v, want %v (warnings %#v)",
					found, test.want, record.Warnings)
			}

			// The value itself is always kept: the warning is a review prompt.
			if test.emails != "" && record.Raw.Value("emails") != test.emails {
				t.Fatalf("raw emails = %q, want the original value", record.Raw.Value("emails"))
			}
		})
	}
}

func TestSharesRegistrableDomainComparesBothDirections(t *testing.T) {
	t.Parallel()

	if !sharesRegistrableDomain("bakery.example", "bakery.example") {
		t.Error("identical domains must match")
	}

	if !sharesRegistrableDomain("mail.bakery.example", "bakery.example") {
		t.Error("a sub-domain of the website must match")
	}

	if !sharesRegistrableDomain("bakery.example", "mail.bakery.example") {
		t.Error("the reverse sub-domain direction must match")
	}

	if sharesRegistrableDomain("notbakery.example", "bakery.example") {
		t.Error("a domain that merely ends with the same text must not match")
	}

	if sharesRegistrableDomain("", "bakery.example") || sharesRegistrableDomain("bakery.example", "") {
		t.Error("an empty domain must never match")
	}
}
