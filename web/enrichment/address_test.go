package enrichment

import (
	"bytes"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

func parseTestDocument(t *testing.T, markup string) *goquery.Document {
	t.Helper()

	document, err := goquery.NewDocumentFromReader(bytes.NewReader([]byte(markup)))
	if err != nil {
		t.Fatalf("parse markup: %v", err)
	}

	return document
}

func TestExtractAddressesReadsStructuredDataMicrodataAndAddressElements(t *testing.T) {
	t.Parallel()

	markup := `<html><body>
	<script type="application/ld+json">
	{"@context":"https://schema.org","@type":"LocalBusiness","name":"Bakery",
	 "address":{"@type":"PostalAddress","streetAddress":"12 Mill Lane",
	 "addressLocality":"Bristol","addressRegion":"Somerset","postalCode":"BS1 4AA",
	 "addressCountry":{"@type":"Country","name":"GB"}}}
	</script>
	<div itemscope itemtype="https://schema.org/PostalAddress">
	  <span itemprop="streetAddress">88 Harbour Road</span>
	  <span itemprop="addressLocality">Cardiff</span>
	  <span itemprop="postalCode">CF10 1AA</span>
	</div>
	<address>440 Church Street, Springfield, IL 62704</address>
	<address>hello@example.com</address>
	</body></html>`

	source := Source{PageURL: "https://example.com/contact", PageKind: PageContact}
	found := extractAddresses(parseTestDocument(t, markup), source)

	if len(found) != 3 {
		t.Fatalf("extracted %d addresses, want 3: %#v", len(found), found)
	}

	byMethod := make(map[ExtractionMethod]PostalAddress, len(found))
	for _, item := range found {
		byMethod[item.source.Method] = item.address
	}

	structured, ok := byMethod[MethodStructuredData]
	if !ok {
		t.Fatal("structured data address is missing")
	}

	if structured.Street != "12 Mill Lane" || structured.Locality != "Bristol" ||
		structured.Region != "Somerset" || structured.PostalCode != "BS1 4AA" ||
		structured.Country != "GB" {
		t.Fatalf("structured address = %#v", structured)
	}

	if structured.Value != "12 Mill Lane, Bristol, Somerset, BS1 4AA, GB" {
		t.Fatalf("structured display value = %q", structured.Value)
	}

	microdata, ok := byMethod[MethodMicrodata]
	if !ok {
		t.Fatal("microdata address is missing")
	}

	if microdata.Street != "88 Harbour Road" || microdata.PostalCode != "CF10 1AA" {
		t.Fatalf("microdata address = %#v", microdata)
	}

	visible, ok := byMethod[MethodVisibleText]
	if !ok {
		t.Fatal("address element is missing")
	}

	if visible.Value != "440 Church Street, Springfield, IL 62704" {
		t.Fatalf("address element value = %q", visible.Value)
	}
}

func TestExtractAddressesIgnoresNonPostalContent(t *testing.T) {
	t.Parallel()

	markup := `<html><body>
	<script type="application/ld+json">{"@type":"Organization","name":"No address here"}</script>
	<address>Written by the team</address>
	<address>hello@example.com</address>
	<p>123 imaginary street mentioned in prose</p>
	</body></html>`

	found := extractAddresses(parseTestDocument(t, markup), Source{PageKind: PageHomepage})
	if len(found) != 0 {
		t.Fatalf("extracted %d addresses from non-postal content: %#v", len(found), found)
	}
}

func TestMergeAddressesDeduplicatesAcrossPagesAndFillsGaps(t *testing.T) {
	t.Parallel()

	homepage := Source{PageURL: "https://example.com/", PageKind: PageHomepage, Method: MethodVisibleText}
	contact := Source{PageURL: "https://example.com/contact", PageKind: PageContact, Method: MethodStructuredData}

	merged := mergeAddresses([]rawAddress{
		{address: PostalAddress{Value: "440 Church Street, Springfield, IL 62704"}, source: homepage},
		{
			address: PostalAddress{
				Value:  "440 Church Street, Springfield, IL 62704",
				Street: "440 Church Street", Locality: "Springfield",
				Region: "IL", PostalCode: "62704",
			},
			source: contact,
		},
	})

	if len(merged) != 1 {
		t.Fatalf("merged %d addresses, want 1: %#v", len(merged), merged)
	}

	if merged[0].Street != "440 Church Street" || merged[0].PostalCode != "62704" {
		t.Fatalf("merged address lost its structured parts: %#v", merged[0])
	}

	if len(merged[0].Sources) != 2 {
		t.Fatalf("merged address has %d sources, want both pages", len(merged[0].Sources))
	}
}

func TestAuditContentReportsWhatTheCrawlFound(t *testing.T) {
	t.Parallel()

	result := Result{
		Pages: []PageResult{
			{
				Kind: PageHomepage, StatusCode: 200, Title: "Bakery",
				MetaDescription: "Fresh bread", MobileViewport: true,
			},
			{Kind: PageContact, StatusCode: 200},
		},
		Phones: []Phone{{Value: "+441234567890", Sources: []Source{{Method: MethodTelephoneLink}}}},
		Emails: []Email{{Address: "hi@example.com", Sources: []Source{{Method: MethodStructuredData}}}},
		Addresses: []PostalAddress{
			{Value: "12 Mill Lane, Bristol"},
		},
	}

	audit := auditContent(&result)

	if !audit.ContactPage {
		t.Error("contact page was crawled but not reported")
	}

	if audit.AboutPage {
		t.Error("about page was reported without being crawled")
	}

	if !audit.VisiblePhone {
		t.Error("a tel: link is a visible phone")
	}

	if audit.VisibleEmail {
		t.Error("an address found only in structured data is not visible")
	}

	if !audit.PostalAddress {
		t.Error("an extracted address was not reported")
	}

	if audit.SocialLinks {
		t.Error("social links were reported without any profile")
	}

	if !audit.PageTitle || !audit.MetaDescription || !audit.MobileViewport {
		t.Errorf("homepage signals were not carried into the audit: %#v", audit)
	}

	if audit.Checked != 9 || audit.Present != 6 {
		t.Fatalf("audit ratio = %d/%d, want 6/9", audit.Present, audit.Checked)
	}
}
