package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gosom/google-maps-scraper/web"
)

// The rescan harness drives the real legacy-CSV import path repeatedly against
// one database, the way a second or third scrape of the same market does. Every
// scenario asserts on the three observable outcomes an operator sees: how many
// live business rows exist, how each row says it was identified
// (businesses.identity_method), and which pending duplicate_candidates were
// filed. Nothing here reaches into resolveBusinessIdentity directly, so a fix
// that only satisfies the unit layer cannot make these pass.

// rescanRow builds one legacy CSV row from a base fixture plus overrides. An
// empty override value clears the column, which is how a rediscovery that lost
// its place_id is expressed.
func rescanRow(base map[string]string, overrides map[string]string) map[string]string {
	row := make(map[string]string, len(base)+len(overrides))
	for key, value := range base {
		row[key] = value
	}
	for key, value := range overrides {
		if value == "" {
			delete(row, key)

			continue
		}
		row[key] = value
	}

	return row
}

// importRescanBatch imports one CSV batch under a fresh job, which is exactly
// what a repeated scrape of the same market produces: identical rows, a new
// job id, a later observation timestamp.
func importRescanBatch(
	t *testing.T,
	repository *repo,
	jobID string,
	offset time.Duration,
	rows []map[string]string,
) web.ResultFileImport {
	t.Helper()

	ctx := context.Background()
	job := resultImportJob(jobID, entityResolutionBase.Add(offset))
	if err := repository.Create(ctx, &job); err != nil {
		t.Fatalf("create job %s: %v", jobID, err)
	}
	path := filepath.Join(t.TempDir(), jobID+".csv")
	writeLegacyResultRows(t, path, rows...)
	summary, err := repository.ImportLegacyCSV(ctx, job, path)
	if err != nil {
		t.Fatalf("import %s: %v", jobID, err)
	}

	return summary
}

// pendingCandidateTotal counts every pending review pair in the database.
func pendingCandidateTotal(t *testing.T, repository *repo) int {
	t.Helper()

	var count int
	if err := repository.db.QueryRowContext(
		context.Background(),
		`SELECT COUNT(*) FROM duplicate_candidates WHERE state = 'pending'`,
	).Scan(&count); err != nil {
		t.Fatalf("count pending duplicate candidates: %v", err)
	}

	return count
}

// pendingCandidatePairs renders the pending review queue as sorted
// "left|right|score" strings so a failure message names the offending pairs.
func pendingCandidatePairs(t *testing.T, repository *repo) []string {
	t.Helper()

	rows, err := repository.db.QueryContext(
		context.Background(),
		`SELECT left_business.name, right_business.name, duplicate_candidates.score
		FROM duplicate_candidates
		JOIN businesses AS left_business ON left_business.id = duplicate_candidates.left_business_id
		JOIN businesses AS right_business ON right_business.id = duplicate_candidates.right_business_id
		WHERE duplicate_candidates.state = 'pending'`,
	)
	if err != nil {
		t.Fatalf("list pending duplicate candidates: %v", err)
	}
	defer func() { _ = rows.Close() }()

	pairs := make([]string, 0)
	for rows.Next() {
		var left, right string
		var score float64
		if err := rows.Scan(&left, &right, &score); err != nil {
			t.Fatalf("scan pending duplicate candidate: %v", err)
		}
		pairs = append(pairs, fmt.Sprintf("%s|%s|%.2f", left, right, score))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read pending duplicate candidates: %v", err)
	}
	sort.Strings(pairs)

	return pairs
}

// identityMethodTally reports the identity_method distribution over live rows.
func identityMethodTally(t *testing.T, repository *repo) map[string]int {
	t.Helper()

	rows, err := repository.db.QueryContext(
		context.Background(),
		`SELECT COALESCE(identity_method, ''), COUNT(*) FROM businesses
		WHERE deleted_at IS NULL AND merged_into_id IS NULL
		GROUP BY COALESCE(identity_method, '')`,
	)
	if err != nil {
		t.Fatalf("tally identity methods: %v", err)
	}
	defer func() { _ = rows.Close() }()

	tally := make(map[string]int)
	for rows.Next() {
		var method string
		var count int
		if err := rows.Scan(&method, &count); err != nil {
			t.Fatalf("scan identity method tally: %v", err)
		}
		tally[method] = count
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read identity method tally: %v", err)
	}

	return tally
}

// totalBusinessVersions counts every stored snapshot; a rescan of unchanged
// data must not add any.
func totalBusinessVersions(t *testing.T, repository *repo) int {
	t.Helper()

	var count int
	if err := repository.db.QueryRowContext(
		context.Background(),
		`SELECT COUNT(*) FROM business_versions`,
	).Scan(&count); err != nil {
		t.Fatalf("count business versions: %v", err)
	}

	return count
}

// totalBusinessChanges counts every recorded field or identity change.
func totalBusinessChanges(t *testing.T, repository *repo) int {
	t.Helper()

	var count int
	if err := repository.db.QueryRowContext(
		context.Background(),
		`SELECT COUNT(*) FROM business_changes`,
	).Scan(&count); err != nil {
		t.Fatalf("count business changes: %v", err)
	}

	return count
}

// identityProvenanceFingerprint renders every live row's provenance so a
// rescan can be asserted to leave it byte-identical.
func identityProvenanceFingerprint(t *testing.T, repository *repo) []string {
	t.Helper()

	rows, err := repository.db.QueryContext(
		context.Background(),
		`SELECT id, COALESCE(identity_method, ''), COALESCE(identity_confidence, 0),
			COALESCE(identity_evidence, '[]')
		FROM businesses WHERE deleted_at IS NULL AND merged_into_id IS NULL
		ORDER BY id`,
	)
	if err != nil {
		t.Fatalf("read identity provenance fingerprint: %v", err)
	}
	defer func() { _ = rows.Close() }()

	fingerprint := make([]string, 0)
	for rows.Next() {
		var id, method, evidence string
		var confidence float64
		if err := rows.Scan(&id, &method, &confidence, &evidence); err != nil {
			t.Fatalf("scan identity provenance fingerprint: %v", err)
		}
		fingerprint = append(fingerprint, fmt.Sprintf("%s|%s|%.3f|%s", id, method, confidence, evidence))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate identity provenance fingerprint: %v", err)
	}

	return fingerprint
}

// businessIDByAddressPrefix finds the single live row whose normalized address
// starts with the given prefix.
func businessIDByAddressPrefix(t *testing.T, repository *repo, prefix string) string {
	t.Helper()

	var id string
	if err := repository.db.QueryRowContext(
		context.Background(),
		`SELECT id FROM businesses
		WHERE deleted_at IS NULL AND merged_into_id IS NULL AND normalized_address LIKE ?`,
		prefix+"%",
	).Scan(&id); err != nil {
		t.Fatalf("find business with address prefix %q: %v", prefix, err)
	}

	return id
}

// metroMarketBatch is a realistic single-market scrape: a five-location
// franchise sharing one call centre number and one website, two independent
// businesses, and two tenants of the same medical building.
func metroMarketBatch() []map[string]string {
	franchise := map[string]string{
		"title": "Front Range Plumbing", "category": "Plumber",
		"phone": "+1 303 555 0100", "website": "https://frontrangeplumbing.example",
	}

	return []map[string]string{
		rescanRow(franchise, map[string]string{
			"place_id": "frp-denver", "address": "100 Denver Way, Denver, CO 80014, United States",
			"latitude": "39.7000", "longitude": "-104.9000",
		}),
		rescanRow(franchise, map[string]string{
			"place_id": "frp-boulder", "address": "200 Boulder Ave, Boulder, CO 80301, United States",
			"latitude": "40.0150", "longitude": "-105.2700",
		}),
		rescanRow(franchise, map[string]string{
			"place_id": "frp-aurora", "address": "300 Aurora Pkwy, Aurora, CO 80012, United States",
			"latitude": "39.7290", "longitude": "-104.8320",
		}),
		rescanRow(franchise, map[string]string{
			"place_id": "frp-lakewood", "address": "400 Lakewood Blvd, Lakewood, CO 80226, United States",
			"latitude": "39.7060", "longitude": "-105.0810",
		}),
		rescanRow(franchise, map[string]string{
			"place_id": "frp-arvada", "address": "500 Arvada Rd, Arvada, CO 80002, United States",
			"latitude": "39.8030", "longitude": "-105.0870",
		}),
		{
			"title": "Mile High Bagels", "category": "Bagel shop", "place_id": "mhb-1",
			"address": "12 Larimer St, Denver, CO 80202, United States",
			"phone":   "+1 303 555 0201", "website": "https://milehighbagels.example",
			"latitude": "39.7500", "longitude": "-104.9990",
		},
		{
			"title": "Capitol Hill Cycles", "category": "Bicycle shop", "place_id": "chc-1",
			"address": "88 Grant St, Denver, CO 80203, United States",
			"phone":   "+1 303 555 0202", "website": "https://capitolhillcycles.example",
			"latitude": "39.7320", "longitude": "-104.9840",
		},
		{
			"title": "Cherry Creek Dental Group", "category": "Dentist", "place_id": "ccd-1",
			"address": "4500 Cherry Creek Dr Suite 210, Denver, CO 80246, United States",
			"phone":   "+1 303 555 0301", "website": "https://cherrycreekdental.example",
			"latitude": "39.7080", "longitude": "-104.9330",
		},
		{
			"title": "Alpine Orthodontics", "category": "Orthodontist", "place_id": "alp-1",
			"address": "4500 Cherry Creek Dr Suite 310, Denver, CO 80246, United States",
			"phone":   "+1 303 555 0302", "website": "https://alpineortho.example",
			"latitude": "39.7080", "longitude": "-104.9330",
		},
	}
}

func TestRescanOfIdenticalMarketBatchIsIdempotent(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	batch := metroMarketBatch()
	importRescanBatch(t, repository, "rescan-idem-1", 0, batch)

	businesses := liveBusinessCount(t, repository)
	if businesses != len(batch) {
		t.Fatalf("first import produced %d rows, want %d", businesses, len(batch))
	}
	provenance := identityProvenanceFingerprint(t, repository)
	candidates := pendingCandidatePairs(t, repository)
	versions := totalBusinessVersions(t, repository)
	changes := totalBusinessChanges(t, repository)

	for pass := 2; pass <= 3; pass++ {
		importRescanBatch(
			t, repository,
			fmt.Sprintf("rescan-idem-%d", pass),
			time.Duration(pass)*time.Hour,
			batch,
		)

		if got := liveBusinessCount(t, repository); got != businesses {
			t.Fatalf("rescan pass %d changed row count: %d, want %d", pass, got, businesses)
		}
		if got := identityProvenanceFingerprint(t, repository); !equalStringSlices(got, provenance) {
			t.Fatalf("rescan pass %d churned provenance:\n got %v\nwant %v", pass, got, provenance)
		}
		if got := pendingCandidatePairs(t, repository); !equalStringSlices(got, candidates) {
			t.Fatalf("rescan pass %d changed the review queue:\n got %v\nwant %v", pass, got, candidates)
		}
		if got := totalBusinessVersions(t, repository); got != versions {
			t.Fatalf("rescan pass %d added %d versions, want a stable %d", pass, got-versions, versions)
		}
		if got := totalBusinessChanges(t, repository); got != changes {
			t.Fatalf("rescan pass %d added %d change rows, want a stable %d", pass, got-changes, changes)
		}
	}
}

func TestRescanWithRefreshedReviewCountsUpdatesInPlace(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	// A real second scrape is never byte-identical: review counts and ratings
	// move. Those rows must update in place, not fork.
	batch := make([]map[string]string, 0, len(metroMarketBatch()))
	for index, row := range metroMarketBatch() {
		batch = append(batch, rescanRow(row, map[string]string{
			"review_count":  strconv.Itoa(100 + index),
			"review_rating": "4.2",
		}))
	}
	importRescanBatch(t, repository, "rescan-fresh-1", 0, batch)

	businesses := liveBusinessCount(t, repository)
	versions := totalBusinessVersions(t, repository)
	provenance := identityProvenanceFingerprint(t, repository)
	candidates := pendingCandidatePairs(t, repository)
	if versions != businesses {
		t.Fatalf("first import wrote %d versions for %d rows, want one each", versions, businesses)
	}

	refreshed := make([]map[string]string, 0, len(batch))
	for index, row := range batch {
		refreshed = append(refreshed, rescanRow(row, map[string]string{
			"review_count":  strconv.Itoa(140 + index),
			"review_rating": "4.4",
		}))
	}
	importRescanBatch(t, repository, "rescan-fresh-2", time.Hour, refreshed)

	if got := liveBusinessCount(t, repository); got != businesses {
		t.Fatalf("refreshed rescan changed the row count: %d, want %d", got, businesses)
	}
	if got := totalBusinessVersions(t, repository); got != versions+businesses {
		t.Fatalf("refreshed rescan wrote %d versions, want exactly one per row", got-versions)
	}
	if got := identityProvenanceFingerprint(t, repository); !equalStringSlices(got, provenance) {
		t.Fatalf("refreshed rescan churned provenance:\n got %v\nwant %v", got, provenance)
	}
	if got := pendingCandidatePairs(t, repository); !equalStringSlices(got, candidates) {
		t.Fatalf("refreshed rescan changed the review queue:\n got %v\nwant %v", got, candidates)
	}

	// The third pass repeats the second: unchanged content adds no version.
	importRescanBatch(t, repository, "rescan-fresh-3", 2*time.Hour, refreshed)
	if got := totalBusinessVersions(t, repository); got != versions+businesses {
		t.Fatalf("repeating an unchanged batch added %d versions, want none", got-versions-businesses)
	}
}

func TestKeylessRediscoveryDoesNotForkOnEveryRescan(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	// The weakest row a scrape can emit: a name and coordinates, no place_id,
	// no phone, no website, and an address too short to key on. Its canonical
	// key falls back to the raw row content, so volatile review counts alone
	// must not fork it into a new row on every pass.
	base := map[string]string{
		"title": "Kiosk 7", "category": "Coffee shop",
		"latitude": "37.7970", "longitude": "-122.3960",
	}

	for pass := 1; pass <= 3; pass++ {
		importRescanBatch(
			t, repository,
			fmt.Sprintf("rescan-keyless-%d", pass),
			time.Duration(pass)*time.Hour,
			[]map[string]string{rescanRow(base, map[string]string{
				"review_count": strconv.Itoa(10 * pass),
			})},
		)
		if got := liveBusinessCount(t, repository); got != 1 {
			t.Fatalf("pass %d forked a keyless rediscovery into %d rows, want 1", pass, got)
		}
	}
	if got := pendingCandidateTotal(t, repository); got != 0 {
		t.Fatalf("keyless rediscovery filed %d review pairs, want none", got)
	}
}

func TestFranchiseRescanKeepsFiveRowsWithoutCandidateSpam(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	batch := metroMarketBatch()[:5]
	importRescanBatch(t, repository, "rescan-chain-1", 0, batch)
	importRescanBatch(t, repository, "rescan-chain-2", time.Hour, batch)

	if got := liveBusinessCount(t, repository); got != 5 {
		t.Fatalf("franchise rows = %d, want 5 distinct locations", got)
	}
	if pairs := pendingCandidatePairs(t, repository); len(pairs) != 0 {
		t.Fatalf(
			"franchise locations filed %d review pairs, want none: %v",
			len(pairs), pairs,
		)
	}

	tally := identityMethodTally(t, repository)
	if tally[identityMethodNew] != 5 {
		t.Fatalf("identity method tally = %v, want 5 rows identified as new", tally)
	}
}

func TestSuiteNeighboursNeverMergeAcrossRescans(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	batch := metroMarketBatch()[7:]
	importRescanBatch(t, repository, "rescan-suite-1", 0, batch)
	importRescanBatch(t, repository, "rescan-suite-2", time.Hour, batch)

	if got := liveBusinessCount(t, repository); got != 2 {
		t.Fatalf("suite neighbours collapsed to %d rows, want 2", got)
	}
	if pairs := pendingCandidatePairs(t, repository); len(pairs) != 0 {
		t.Fatalf("suite neighbours filed %d review pairs, want none: %v", len(pairs), pairs)
	}
}

func TestPlaceIDDriftAcrossRescanKeepsOneRow(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	base := map[string]string{
		"title": "Sunrise Veterinary", "category": "Veterinarian",
		"address": "77 Sunrise Ln, Boise, ID 83702, United States",
		"phone":   "+1 208 555 0100", "website": "https://sunrisevet.example",
		"latitude": "43.6180", "longitude": "-116.2000",
	}

	importRescanBatch(t, repository, "rescan-drift-1", 0, []map[string]string{
		rescanRow(base, map[string]string{"place_id": "sunrise-old"}),
	})
	importRescanBatch(t, repository, "rescan-drift-2", time.Hour, []map[string]string{
		rescanRow(base, map[string]string{"place_id": "sunrise-new"}),
	})

	if got := liveBusinessCount(t, repository); got != 1 {
		t.Fatalf("place_id drift produced %d rows, want 1", got)
	}
	id := businessIDByPlaceKey(t, repository, "sunrise-old")
	if other := businessIDByPlaceKey(t, repository, "sunrise-new"); other != id {
		t.Fatalf("drifted place_ids resolve to %s and %s, want one row", id, other)
	}
	if pairs := pendingCandidatePairs(t, repository); len(pairs) != 0 {
		t.Fatalf("place_id drift filed %d review pairs, want none: %v", len(pairs), pairs)
	}

	// A third pass carrying the drifted id is a pure exact match: no second
	// drift audit entry, no provenance rewrite.
	provenance := identityProvenanceFingerprint(t, repository)
	importRescanBatch(t, repository, "rescan-drift-3", 2*time.Hour, []map[string]string{
		rescanRow(base, map[string]string{"place_id": "sunrise-new"}),
	})
	if drifts := identityDriftChangeCount(t, repository, id); drifts != 1 {
		t.Fatalf("identity_drift entries = %d, want exactly 1", drifts)
	}
	if got := identityProvenanceFingerprint(t, repository); !equalStringSlices(got, provenance) {
		t.Fatalf("exact rescan rewrote provenance:\n got %v\nwant %v", got, provenance)
	}
}

func TestMissingIdentifiersAttachOnlyWhenCorroborated(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	anchor := map[string]string{
		"title": "Willow Tea House", "category": "Tea house", "place_id": "willow-1",
		"address": "9 Willow St, Providence, RI 02903, United States",
		"phone":   "+1 401 555 0100", "website": "https://willowtea.example",
		"latitude": "41.8240", "longitude": "-71.4130",
	}
	importRescanBatch(t, repository, "rescan-missing-1", 0, []map[string]string{anchor})
	anchorID := businessIDByPlaceKey(t, repository, "willow-1")

	// Corroborated rediscovery: no place_id, but same name, phone, address and
	// coordinates. It must land on the existing row.
	corroborated := rescanRow(anchor, map[string]string{"place_id": ""})
	importRescanBatch(t, repository, "rescan-missing-2", time.Hour, []map[string]string{corroborated})

	if got := liveBusinessCount(t, repository); got != 1 {
		t.Fatalf("corroborated rediscovery produced %d rows, want 1", got)
	}
	method, confidence, _ := businessIdentity(t, repository, anchorID)
	if method != identityMethodPhone || confidence != identityPhoneConfidence {
		t.Fatalf("identity = %q/%v, want %s/%v", method, confidence, identityMethodPhone, identityPhoneConfidence)
	}

	// Weak evidence: a different business three kilometres away that only
	// shares a postal code and a loosely similar name must stay separate.
	weak := map[string]string{
		"title": "Willow Tea Room", "category": "Tea house",
		"address":  "4100 Hope St, Providence, RI 02903, United States",
		"latitude": "41.8510", "longitude": "-71.4130",
	}
	importRescanBatch(t, repository, "rescan-missing-3", 2*time.Hour, []map[string]string{weak})

	if got := liveBusinessCount(t, repository); got != 2 {
		t.Fatalf("weak evidence merged rows: %d live rows, want 2", got)
	}
}

func TestNearDuplicateNamesSurviveRepeatedRescans(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	joes := map[string]string{
		"title": "Joe's Pizza", "category": "Pizza restaurant", "place_id": "joes-1",
		"address":  "7 Carmine St, New York, NY 10014, United States",
		"phone":    "+1 212 555 0100",
		"latitude": "40.7304", "longitude": "-74.0026",
	}
	joesTwo := map[string]string{
		"title": "Joes Pizza II", "category": "Pizza restaurant", "place_id": "joes-2",
		"address":  "150 Bleecker St, New York, NY 10012, United States",
		"phone":    "+1 212 555 0199",
		"latitude": "40.7311", "longitude": "-74.0026",
	}

	batch := []map[string]string{joes, joesTwo}
	for pass := 1; pass <= 3; pass++ {
		importRescanBatch(
			t, repository,
			fmt.Sprintf("rescan-near-%d", pass),
			time.Duration(pass)*time.Hour,
			batch,
		)
		if got := liveBusinessCount(t, repository); got != 2 {
			t.Fatalf("pass %d collapsed near-duplicate names into %d rows, want 2", pass, got)
		}
	}
	if got := pendingCandidateTotal(t, repository); got > 1 {
		t.Fatalf("near-duplicate names filed %d pending pairs, want at most 1", got)
	}
}

func TestChangedContactDataFollowsUpdatePathWithoutCandidates(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	before := map[string]string{
		"title": "Northside Barbers", "category": "Barber shop", "place_id": "north-1",
		"address": "5 Old Mill Rd, Nashville, TN 37206, United States",
		"phone":   "+1 615 555 0100", "website": "https://northsidebarbers.example",
		"latitude": "36.1800", "longitude": "-86.7400",
	}
	rebranded := map[string]string{
		"title": "Northside Barbers & Co", "category": "Barber shop", "place_id": "north-1",
		"address": "410 Gallatin Ave, Nashville, TN 37206, United States",
		"phone":   "+1 615 555 0777", "website": "https://northsidebarbershop.example",
		"latitude": "36.1830", "longitude": "-86.7460",
	}

	importRescanBatch(t, repository, "rescan-moved-1", 0, []map[string]string{before})
	importRescanBatch(t, repository, "rescan-moved-2", time.Hour, []map[string]string{rebranded})

	if got := liveBusinessCount(t, repository); got != 1 {
		t.Fatalf("a moved business produced %d rows, want 1", got)
	}
	if pairs := pendingCandidatePairs(t, repository); len(pairs) != 0 {
		t.Fatalf("a moved business filed %d review pairs, want none: %v", len(pairs), pairs)
	}

	id := businessIDByPlaceKey(t, repository, "north-1")
	method, confidence, _ := businessIdentity(t, repository, id)
	if method != identityMethodNew || confidence != identityExactConfidence {
		t.Fatalf("identity after move = %q/%v, want %s/1", method, confidence, identityMethodNew)
	}

	var address, phone, domain string
	if err := repository.db.QueryRowContext(
		context.Background(),
		`SELECT address, COALESCE(normalized_phone, ''), COALESCE(domain, '')
		FROM businesses WHERE id = ?`,
		id,
	).Scan(&address, &phone, &domain); err != nil {
		t.Fatalf("read moved business: %v", err)
	}
	if !strings.HasPrefix(address, "410 Gallatin Ave") {
		t.Fatalf("address = %q, want the new location", address)
	}
	if !strings.HasSuffix(phone, "6155550777") {
		t.Fatalf("normalized_phone = %q, want the new number", phone)
	}
	if domain != "northsidebarbershop.example" {
		t.Fatalf("domain = %q, want the rebranded domain", domain)
	}
}

func TestImportOrderDoesNotChangeMergeDecisions(t *testing.T) {
	t.Parallel()

	// Batch A is the first scrape of a market. Batch B is a second scrape that
	// drifts one place_id, drops another, and adds a near neighbour.
	batchA := []map[string]string{
		{
			"title": "Ridgeway Optical", "category": "Optometrist", "place_id": "ridge-a",
			"address": "20 Ridgeway Ave, Madison, WI 53703, United States",
			"phone":   "+1 608 555 0100", "website": "https://ridgewayoptical.example",
			"latitude": "43.0740", "longitude": "-89.3840",
		},
		{
			"title": "Lakeview Hardware", "category": "Hardware store", "place_id": "lake-a",
			"address":  "31 Lakeview St, Madison, WI 53703, United States",
			"phone":    "+1 608 555 0200",
			"latitude": "43.0760", "longitude": "-89.3800",
		},
	}
	batchB := []map[string]string{
		{
			"title": "Ridgeway Optical", "category": "Optometrist", "place_id": "ridge-b",
			"address": "20 Ridgeway Ave, Madison, WI 53703, United States",
			"phone":   "+1 608 555 0100", "website": "https://ridgewayoptical.example",
			"latitude": "43.0740", "longitude": "-89.3840",
		},
		{
			"title": "Lakeview Hardware", "category": "Hardware store",
			"address":  "31 Lakeview St, Madison, WI 53703, United States",
			"phone":    "+1 608 555 0200",
			"latitude": "43.0760", "longitude": "-89.3800",
		},
	}

	forward, forwardCandidates := runOrderedRescan(t, "fwd", batchA, batchB)
	reverse, reverseCandidates := runOrderedRescan(t, "rev", batchB, batchA)

	if forward != reverse {
		t.Fatalf("import order changed the row count: A->B = %d, B->A = %d", forward, reverse)
	}
	if !equalStringSlices(forwardCandidates, reverseCandidates) {
		t.Fatalf(
			"import order changed the review queue:\n A->B %v\n B->A %v",
			forwardCandidates, reverseCandidates,
		)
	}
}

// runOrderedRescan imports two batches in the given order into a fresh
// database and returns the live row count plus the pending review pairs.
func runOrderedRescan(
	t *testing.T,
	label string,
	first, second []map[string]string,
) (int, []string) {
	t.Helper()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	importRescanBatch(t, repository, "rescan-order-"+label+"-1", 0, first)
	importRescanBatch(t, repository, "rescan-order-"+label+"-2", time.Hour, second)

	return liveBusinessCount(t, repository), pendingCandidatePairs(t, repository)
}

func TestMixedMarketOrderProducesTheSameDecisions(t *testing.T) {
	t.Parallel()

	// Each batch carries one half of three independent decisions: a franchise
	// pair that must stay apart, a drifted place_id that must collapse, and a
	// near-duplicate name pair that must stay apart. Whichever half lands
	// first, the outcome has to be identical.
	franchise := map[string]string{
		"title": "Gulf Coast Tyres", "category": "Tire shop",
		"phone": "+1 713 555 0100", "website": "https://gulfcoasttyres.example",
	}
	batchA := []map[string]string{
		rescanRow(franchise, map[string]string{
			"place_id": "gct-houston", "address": "1 Bay Area Blvd, Houston, TX 77058, United States",
			"latitude": "29.5600", "longitude": "-95.0900",
		}),
		{
			"title": "Bayou Bakery", "category": "Bakery", "place_id": "bayou-old",
			"address": "44 Bayou St, Houston, TX 77007, United States",
			"phone":   "+1 713 555 0200", "website": "https://bayoubakery.example",
			"latitude": "29.7700", "longitude": "-95.4000",
		},
		{
			"title": "Heights Barbecue", "category": "Barbecue restaurant", "place_id": "heights-1",
			"address":  "80 Heights Blvd, Houston, TX 77007, United States",
			"phone":    "+1 713 555 0300",
			"latitude": "29.7860", "longitude": "-95.3980",
		},
	}
	batchB := []map[string]string{
		rescanRow(franchise, map[string]string{
			"place_id": "gct-katy", "address": "2 Katy Fwy, Katy, TX 77494, United States",
			"latitude": "29.7850", "longitude": "-95.8240",
		}),
		{
			"title": "Bayou Bakery", "category": "Bakery", "place_id": "bayou-new",
			"address": "44 Bayou St, Houston, TX 77007, United States",
			"phone":   "+1 713 555 0200", "website": "https://bayoubakery.example",
			"latitude": "29.7700", "longitude": "-95.4000",
		},
		{
			"title": "Heights Barbecue Co", "category": "Barbecue restaurant", "place_id": "heights-2",
			"address":  "82 Heights Blvd, Houston, TX 77007, United States",
			"phone":    "+1 713 555 0399",
			"latitude": "29.7861", "longitude": "-95.3980",
		},
	}

	forward, forwardCandidates := runOrderedRescan(t, "mixed-fwd", batchA, batchB)
	reverse, reverseCandidates := runOrderedRescan(t, "mixed-rev", batchB, batchA)

	// Two franchise rows, one bakery after the drift, two barbecue rows.
	const wantRows = 5
	if forward != wantRows || reverse != wantRows {
		t.Fatalf("row counts A->B = %d, B->A = %d, want %d each", forward, reverse, wantRows)
	}
	if !equalStringSlices(forwardCandidates, reverseCandidates) {
		t.Fatalf(
			"import order changed the review queue:\n A->B %v\n B->A %v",
			forwardCandidates, reverseCandidates,
		)
	}
}

func TestExactKeyCandidateRespectsKeepSeparateRule(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	anchor := map[string]string{
		"title": "Harbor Dental", "category": "Dentist", "place_id": "harbor-anchor",
		"address":  "500 Mission St, San Francisco, CA 94105, United States",
		"phone":    "+1 415 555 0100",
		"latitude": "37.7880", "longitude": "-122.3970",
	}
	neighbour := map[string]string{
		"title": "Presidio Dental", "category": "Dentist", "place_id": "presidio-1",
		"address":  "3000 Lombard St, San Francisco, CA 94123, United States",
		"phone":    "+1 415 555 0200",
		"latitude": "37.7990", "longitude": "-122.4430",
	}

	batch := []map[string]string{anchor, neighbour}
	importRescanBatch(t, repository, "rescan-exact-1", 0, batch)

	anchorID := businessIDByPlaceKey(t, repository, "harbor-anchor")
	neighbourID := businessIDByPlaceKey(t, repository, "presidio-1")

	// A workspace migrated from a legacy database can hold two rows carrying
	// the same Google place_id, because the legacy schema had no uniqueness
	// there. That is the only way the exact fast path sees two matches.
	if _, err := repository.db.ExecContext(
		ctx,
		`INSERT INTO business_identity_keys(business_id, key_type, key_value, confidence, created_at)
		VALUES (?, 'place_id', 'harbor-anchor', 1, ?)`,
		neighbourID,
		entityResolutionBase.Unix(),
	); err != nil {
		t.Fatalf("seed the migrated duplicate key: %v", err)
	}

	importRescanBatch(t, repository, "rescan-exact-2", time.Hour, []map[string]string{anchor})
	if got := pendingCandidateCount(t, repository, anchorID, neighbourID); got != 1 {
		t.Fatalf("shared place_id filed %d candidates, want 1", got)
	}

	var candidateID int64
	if err := repository.db.QueryRowContext(
		ctx,
		`SELECT id FROM duplicate_candidates WHERE state = 'pending'
			AND ((left_business_id = ? AND right_business_id = ?)
				OR (left_business_id = ? AND right_business_id = ?))`,
		anchorID, neighbourID, neighbourID, anchorID,
	).Scan(&candidateID); err != nil {
		t.Fatalf("read the exact-key candidate: %v", err)
	}
	if _, err := repository.ResolveDuplicateCandidate(ctx, web.DuplicateDecision{
		CandidateID: candidateID, Action: "keep_both", Note: "legacy key collision",
	}); err != nil {
		t.Fatalf("record keep_both decision: %v", err)
	}
	if _, err := repository.db.ExecContext(
		ctx, `DELETE FROM duplicate_candidates WHERE id = ?`, candidateID,
	); err != nil {
		t.Fatalf("clear the resolved candidate: %v", err)
	}

	importRescanBatch(t, repository, "rescan-exact-3", 2*time.Hour, []map[string]string{anchor})
	if got := pendingCandidateCount(t, repository, anchorID, neighbourID); got != 0 {
		t.Fatalf("keep_separate rule ignored: the exact path re-filed %d candidates", got)
	}
	if got := liveBusinessCount(t, repository); got != 2 {
		t.Fatalf("live rows = %d, want 2", got)
	}
}

func TestKeepSeparateRuleSurvivesRepeatedRescans(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	left := map[string]string{
		"title": "Summit Coffee", "category": "Coffee shop", "place_id": "summit-1",
		"address": "10 Summit Ave, Asheville, NC 28801, United States",
		"phone":   "+1 828 555 0100", "website": "https://summitcoffee.example",
		"latitude": "35.5950", "longitude": "-82.5510",
	}
	right := map[string]string{
		"title": "Summit Coffee Roastery", "category": "Coffee roasters", "place_id": "summit-2",
		"address": "12 Summit Ave, Asheville, NC 28801, United States",
		"phone":   "+1 828 555 0100", "website": "https://summitcoffee.example",
		"latitude": "35.5951", "longitude": "-82.5510",
	}

	batch := []map[string]string{left, right}
	importRescanBatch(t, repository, "rescan-rule-1", 0, batch)

	leftID := businessIDByPlaceKey(t, repository, "summit-1")
	rightID := businessIDByPlaceKey(t, repository, "summit-2")
	if leftID == rightID {
		t.Fatalf("fixture collapsed before the operator could rule on it")
	}

	var candidateID int64
	if err := repository.db.QueryRowContext(
		ctx,
		`SELECT id FROM duplicate_candidates WHERE state = 'pending'
			AND ((left_business_id = ? AND right_business_id = ?)
				OR (left_business_id = ? AND right_business_id = ?))`,
		leftID, rightID, rightID, leftID,
	).Scan(&candidateID); err != nil {
		t.Fatalf("read the candidate the operator rules on: %v", err)
	}
	if _, err := repository.ResolveDuplicateCandidate(ctx, web.DuplicateDecision{
		CandidateID: candidateID, Action: "keep_both", Note: "roastery is a separate entity",
	}); err != nil {
		t.Fatalf("record keep_both decision: %v", err)
	}
	// Retention or a manual cleanup can remove the resolved row; only the
	// dedup rule is durable, so the rule alone has to hold the line.
	if _, err := repository.db.ExecContext(
		ctx, `DELETE FROM duplicate_candidates WHERE id = ?`, candidateID,
	); err != nil {
		t.Fatalf("clear the resolved candidate: %v", err)
	}

	for pass := 2; pass <= 4; pass++ {
		importRescanBatch(
			t, repository,
			fmt.Sprintf("rescan-rule-%d", pass),
			time.Duration(pass)*time.Hour,
			batch,
		)
		if got := liveBusinessCount(t, repository); got != 2 {
			t.Fatalf("pass %d merged a keep_separate pair: %d rows, want 2", pass, got)
		}
		if got := pendingCandidateCount(t, repository, leftID, rightID); got != 0 {
			t.Fatalf("pass %d re-filed %d candidates for a keep_separate pair", pass, got)
		}
	}
}

func TestFranchiseWithoutCoordinatesStaysSeparate(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	batch := make([]map[string]string, 0, 5)
	for _, row := range metroMarketBatch()[:5] {
		batch = append(batch, rescanRow(row, map[string]string{
			"latitude": "", "longitude": "",
		}))
	}

	importRescanBatch(t, repository, "rescan-nocoord-1", 0, batch)
	importRescanBatch(t, repository, "rescan-nocoord-2", time.Hour, batch)

	if got := liveBusinessCount(t, repository); got != 5 {
		t.Fatalf("franchise rows without coordinates = %d, want 5 distinct locations", got)
	}
	seen := make(map[string]struct{}, 5)
	for _, prefix := range []string{
		"100 denver way", "200 boulder ave", "300 aurora pkwy",
		"400 lakewood blvd", "500 arvada rd",
	} {
		id := businessIDByAddressPrefix(t, repository, prefix)
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("two franchise addresses resolved to the same row %s", id)
		}
		seen[id] = struct{}{}
	}
}

func TestSharedBuildingPhoneDoesNotMergeDistinctTenants(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	// One medical building, one switchboard number, two unrelated practices
	// whose listings do not spell out the suite.
	dentist := map[string]string{
		"title": "Cherry Creek Dental Group", "category": "Dentist", "place_id": "tenant-dental",
		"address":  "4500 Cherry Creek Dr, Denver, CO 80246, United States",
		"phone":    "+1 303 555 0400",
		"latitude": "39.7080", "longitude": "-104.9330",
	}
	orthodontist := map[string]string{
		"title": "Alpine Orthodontics", "category": "Orthodontist", "place_id": "tenant-ortho",
		"address":  "4500 Cherry Creek Dr, Denver, CO 80246, United States",
		"phone":    "+1 303 555 0400",
		"latitude": "39.7080", "longitude": "-104.9330",
	}

	batch := []map[string]string{dentist, orthodontist}
	importRescanBatch(t, repository, "rescan-tenant-1", 0, batch)
	importRescanBatch(t, repository, "rescan-tenant-2", time.Hour, batch)

	if got := liveBusinessCount(t, repository); got != 2 {
		t.Fatalf("building tenants collapsed to %d rows, want 2", got)
	}
	if left, right := businessIDByPlaceKey(t, repository, "tenant-dental"),
		businessIDByPlaceKey(t, repository, "tenant-ortho"); left == right {
		t.Fatalf("distinct place_ids resolved to the same row %s", left)
	}
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}

	return true
}

func TestKeylessListingDoesNotForkOnEveryRescan(t *testing.T) {
	t.Parallel()

	repository, _, closeRepository := newLocalFeatureRepository(t)
	defer closeRepository()

	// A listing with no place_id, no cid, no data_id, no phone, no website and
	// no usable address has no identity key at all, so its row id is derived
	// from the raw CSV content. A review count that ticks up between scrapes
	// therefore changes the row id, and nothing but name and position is left
	// to recognise the listing by.
	listing := func(reviewCount string) map[string]string {
		return map[string]string{
			"title": "Riverbend Lookout", "category": "Scenic spot",
			"latitude": "44.0520", "longitude": "-121.3150",
			"review_count": reviewCount,
		}
	}

	for pass, reviews := range []string{"10", "12", "15"} {
		importRescanBatch(
			t, repository,
			fmt.Sprintf("rescan-keyless-%d", pass+1),
			time.Duration(pass)*time.Hour,
			[]map[string]string{listing(reviews)},
		)
	}

	if got := liveBusinessCount(t, repository); got != 1 {
		t.Fatalf("a keyless listing forked into %d rows across three rescans, want 1", got)
	}
	if pairs := pendingCandidatePairs(t, repository); len(pairs) != 0 {
		t.Fatalf("a keyless listing filed %d review pairs, want none: %v", len(pairs), pairs)
	}

	var reviewCount int64
	if err := repository.db.QueryRowContext(
		context.Background(),
		`SELECT COALESCE(review_count, 0) FROM businesses
		WHERE deleted_at IS NULL AND merged_into_id IS NULL`,
	).Scan(&reviewCount); err != nil {
		t.Fatalf("read the surviving row: %v", err)
	}
	if reviewCount != 15 {
		t.Fatalf("review_count = %d, want the newest observation's 15", reviewCount)
	}
}
