package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gosom/google-maps-scraper/web/resultimport"
)

// Entity resolution decides which stored business an imported observation
// belongs to. Authoritative Google identifiers (place_id, cid, data_id)
// remain an exact, always-trusted fast path. Phone, domain, and address are
// corroborating signals only: alone they routinely over-merge (a chain's
// shared phone or website folds distinct locations together), so each one
// must be confirmed by name similarity or geographic proximity before an
// automatic attach, and everything in the uncertain band becomes a pending
// duplicate_candidates pair for human review instead of a merge.

// Identity resolution methods persisted to businesses.identity_method.
const (
	identityMethodExact         = "exact"
	identityMethodNew           = "new"
	identityMethodPhone         = "phone_corroborated"
	identityMethodDomain        = "domain_proximity"
	identityMethodAddressName   = "address_name"
	identityMethodNameProximity = "name_proximity"
)

// Confidence tiers and corroboration thresholds. The values are part of the
// resolution contract: >= identityAttachConfidence attaches automatically
// when no authoritative key conflicts, >= identityDriftConfidence is strong
// enough to absorb a drifted or missing place_id, and the candidate band
// files a review pair without touching either record.
const (
	identityExactConfidence   = 1.0
	identityPhoneConfidence   = 0.9
	identityDomainConfidence  = 0.85
	identityAddressConfidence = 0.85
	identityNameConfidence    = 0.75

	identityAttachConfidence    = 0.85
	identityDriftConfidence     = 0.9
	identityCandidateConfidence = 0.6

	identityPhoneNameSimilarity   = 0.7
	identityPhoneDistanceMetres   = 500.0
	identityDomainDistanceMetres  = 300.0
	identityDomainNameSimilarity  = 0.5
	identityAddressNameSimilarity = 0.7
	identityNameSimilarityFloor   = 0.85
	identityNameDistanceMetres    = 150.0

	identityChainDistanceMetres  = 1000.0
	identityChainNameSimilarity  = 0.9
	identityMergeRedirectMaxHops = 5

	// identityPhoneUnrelatedNameFloor is the weakest name agreement the
	// proximity-only phone tier tolerates. A shared switchboard number puts
	// every tenant of one building at the same coordinates, so proximity on
	// its own would fold unrelated neighbours together. A rebrand ("Joe's
	// Pizza" -> "Tony's Pizza") still clears this floor through the shared
	// trade word, while two unrelated practices share no name token at all.
	identityPhoneUnrelatedNameFloor = 0.3

	// identityDriftNameSimilarity is the name agreement required before a
	// corroborated attach may overrule a disagreeing Google identifier. A
	// reassigned place_id does not rename or move the business.
	identityDriftNameSimilarity = 0.9
)

// identityEvidence is one persisted piece of resolution evidence. It is
// serialized into businesses.identity_evidence and duplicate_candidates
// signals as [{"signal","value","detail"}].
type identityEvidence struct {
	Signal string `json:"signal"`
	Value  string `json:"value,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// identityCandidate is an uncertain match that becomes a pending review pair.
type identityCandidate struct {
	businessID string
	score      float64
	evidence   []identityEvidence
}

// identityResolution is the deterministic outcome for one imported record.
type identityResolution struct {
	// businessID is the attach target; empty keeps the incoming row separate.
	businessID string
	method     string
	confidence float64
	evidence   []identityEvidence
	// exactIDs lists every business matched by an authoritative key so the
	// historical exact duplicate pairs keep being filed.
	exactIDs []string
	// drift reports that a fallback attach carries authoritative keys the
	// target does not have yet (place-ID drift or a first-time identifier).
	drift       bool
	driftBefore []string
	driftAfter  []string
	// candidates are uncertain matches for the duplicate review queue.
	candidates []identityCandidate
}

// fallbackRow is a stored business examined by the corroboration tiers.
type fallbackRow struct {
	id                string
	name              string
	normalizedName    string
	normalizedAddress string
	city              string
	postalCode        string
	placeID           string
	cid               string
	dataID            string
	latitude          sql.NullFloat64
	longitude         sql.NullFloat64
	mergedInto        string

	phoneMatch   bool
	phoneValue   string
	domainMatch  bool
	domainValue  string
	addressMatch bool
	addressValue string
	nameProbe    bool
}

// resolveBusinessIdentity matches one normalized import record against the
// stored businesses. It only reads; every write stays in the import path so
// re-importing the same CSV is a no-op.
func resolveBusinessIdentity(
	ctx context.Context,
	tx *sql.Tx,
	business resultimport.Business,
) (identityResolution, error) {
	exactIDs, exactEvidence, err := matchAuthoritativeBusinesses(ctx, tx, business)
	if err != nil {
		return identityResolution{}, err
	}
	if len(exactIDs) > 0 {
		return identityResolution{
			businessID: exactIDs[0],
			method:     identityMethodExact,
			confidence: identityExactConfidence,
			evidence:   exactEvidence,
			exactIDs:   exactIDs,
		}, nil
	}

	rows, err := collectFallbackRows(ctx, tx, business)
	if err != nil {
		return identityResolution{}, err
	}

	return scoreFallbackRows(ctx, tx, business, rows)
}

// authoritativeIdentityKeys returns the incoming record's Google identifiers.
func authoritativeIdentityKeys(business resultimport.Business) []resultimport.IdentityKey {
	keys := make([]resultimport.IdentityKey, 0, 3)
	for _, key := range business.IdentityKeys {
		switch key.Kind {
		case resultimport.IdentityPlaceID, resultimport.IdentityCID, resultimport.IdentityDataID:
			keys = append(keys, key)
		}
	}

	return keys
}

// matchAuthoritativeBusinesses is the unchanged exact fast path: place_id,
// cid, and data_id matches, first against recorded identity keys and then
// against the migrated legacy columns.
func matchAuthoritativeBusinesses(
	ctx context.Context,
	tx *sql.Tx,
	business resultimport.Business,
) ([]string, []identityEvidence, error) {
	matched := make([]string, 0)
	seen := make(map[string]struct{})
	evidence := make([]identityEvidence, 0)

	for _, key := range authoritativeIdentityKeys(business) {
		rows, err := tx.QueryContext(
			ctx,
			`SELECT business_id FROM business_identity_keys
			WHERE key_type = ? AND key_value = ? ORDER BY created_at, business_id`,
			key.Kind,
			key.Value,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("match business identity: %w", err)
		}
		found := false
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()

				return nil, nil, fmt.Errorf("scan business identity: %w", err)
			}
			found = true
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				matched = append(matched, id)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()

			return nil, nil, fmt.Errorf("read business identity matches: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, nil, fmt.Errorf("close business identity rows: %w", err)
		}
		if found {
			evidence = append(evidence, identityEvidence{Signal: string(key.Kind), Value: key.Value})
		}
	}
	if len(matched) > 0 {
		return matched, evidence, nil
	}

	legacyProbes := []struct {
		column string
		signal string
		value  string
	}{
		{column: "place_id", signal: string(resultimport.IdentityPlaceID), value: business.PlaceID},
		{column: "cid", signal: string(resultimport.IdentityCID), value: business.CID},
		{column: "data_id", signal: string(resultimport.IdentityDataID), value: business.DataID},
	}
	for _, probe := range legacyProbes {
		if probe.value == "" {
			continue
		}
		query := `SELECT id FROM businesses WHERE ` + probe.column +
			` = ? AND deleted_at IS NULL ORDER BY created_at, id LIMIT 1`
		var id string
		err := tx.QueryRowContext(ctx, query, probe.value).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("match migrated business by %s: %w", probe.column, err)
		}

		return []string{id},
			[]identityEvidence{{Signal: probe.signal, Value: probe.value, Detail: "legacy column"}},
			nil
	}

	return nil, nil, nil
}

// collectFallbackRows gathers every stored business that shares a phone,
// domain, address, or comparable name with the incoming record, in a stable
// deterministic order, following human merges to the surviving row.
func collectFallbackRows(
	ctx context.Context,
	tx *sql.Tx,
	business resultimport.Business,
) ([]*fallbackRow, error) {
	ordered := make([]*fallbackRow, 0)
	index := make(map[string]*fallbackRow)

	collect := func(query, signal, value string, args ...any) error {
		if value == "" {
			return nil
		}
		found, err := queryFallbackRows(ctx, tx, query, args...)
		if err != nil {
			return err
		}
		for _, row := range found {
			row, err = redirectMergedRow(ctx, tx, row)
			if err != nil {
				return err
			}
			if row == nil {
				continue
			}
			existing, ok := index[row.id]
			if !ok {
				index[row.id] = row
				ordered = append(ordered, row)
				existing = row
			}
			switch signal {
			case "phone":
				existing.phoneMatch = true
				existing.phoneValue = value
			case "domain":
				existing.domainMatch = true
				existing.domainValue = value
			case "address":
				existing.addressMatch = true
				existing.addressValue = value
			case "name":
				existing.nameProbe = true
			}
		}

		return nil
	}

	const keyProbe = `SELECT ` + fallbackRowColumns + `
		FROM businesses
		JOIN business_identity_keys ON business_identity_keys.business_id = businesses.id
		WHERE business_identity_keys.key_type = ? AND business_identity_keys.key_value = ?
			AND businesses.deleted_at IS NULL
		ORDER BY businesses.created_at, businesses.id LIMIT ` + fallbackProbeLimit
	const columnProbe = `SELECT ` + fallbackRowColumns + `
		FROM businesses WHERE %s = ? AND businesses.deleted_at IS NULL
		ORDER BY businesses.created_at, businesses.id LIMIT ` + fallbackProbeLimit

	for _, phone := range business.Phones {
		if err := collect(keyProbe, "phone", phone.MatchKey, resultimport.IdentityPhone, phone.MatchKey); err != nil {
			return nil, err
		}
		if err := collect(
			fmt.Sprintf(columnProbe, "businesses.normalized_phone"),
			"phone", phone.Normalized, phone.Normalized,
		); err != nil {
			return nil, err
		}
	}
	domain := business.Website.Domain
	if err := collect(keyProbe, "domain", domain, resultimport.IdentityDomain, domain); err != nil {
		return nil, err
	}
	if err := collect(fmt.Sprintf(columnProbe, "businesses.domain"), "domain", domain, domain); err != nil {
		return nil, err
	}
	address := business.Address.Normalized
	if err := collect(keyProbe, "address", address, resultimport.IdentityAddress, address); err != nil {
		return nil, err
	}
	if err := collect(
		fmt.Sprintf(columnProbe, "businesses.normalized_address"),
		"address", address, address,
	); err != nil {
		return nil, err
	}

	if business.NormalizedName != "" {
		nameProbe := `SELECT ` + fallbackRowColumns + `
			FROM businesses
			WHERE businesses.deleted_at IS NULL AND (
				businesses.normalized_name = ?
				OR (? <> '' AND businesses.city = ?
					AND substr(businesses.normalized_name, 1, 6) = substr(?, 1, 6))
			)
			ORDER BY businesses.created_at, businesses.id LIMIT ` + fallbackProbeLimit
		if err := collect(
			nameProbe, "name", business.NormalizedName,
			business.NormalizedName,
			business.Address.City,
			business.Address.City,
			business.NormalizedName,
		); err != nil {
			return nil, err
		}
	}

	return ordered, nil
}

const fallbackRowColumns = `businesses.id, businesses.name, businesses.normalized_name,
	COALESCE(businesses.normalized_address, ''), COALESCE(businesses.city, ''),
	COALESCE(businesses.postal_code, ''), COALESCE(businesses.place_id, ''),
	COALESCE(businesses.cid, ''), COALESCE(businesses.data_id, ''),
	businesses.latitude, businesses.longitude, COALESCE(businesses.merged_into_id, '')`

const fallbackProbeLimit = "100"

func queryFallbackRows(
	ctx context.Context,
	tx *sql.Tx,
	query string,
	args ...any,
) ([]*fallbackRow, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("probe fallback identity candidates: %w", err)
	}
	found := make([]*fallbackRow, 0)
	for rows.Next() {
		row := &fallbackRow{}
		if err := rows.Scan(
			&row.id,
			&row.name,
			&row.normalizedName,
			&row.normalizedAddress,
			&row.city,
			&row.postalCode,
			&row.placeID,
			&row.cid,
			&row.dataID,
			&row.latitude,
			&row.longitude,
			&row.mergedInto,
		); err != nil {
			_ = rows.Close()

			return nil, fmt.Errorf("scan fallback identity candidate: %w", err)
		}
		found = append(found, row)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()

		return nil, fmt.Errorf("read fallback identity candidates: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close fallback identity candidates: %w", err)
	}

	return found, nil
}

// redirectMergedRow follows human merge decisions to the surviving business so
// a re-observed merged listing lands on the live row and never resurrects the
// merged one. A nil result drops the candidate.
func redirectMergedRow(ctx context.Context, tx *sql.Tx, row *fallbackRow) (*fallbackRow, error) {
	if row.mergedInto == "" {
		return row, nil
	}
	id := row.mergedInto
	for hop := 0; hop < identityMergeRedirectMaxHops; hop++ {
		reloaded := &fallbackRow{}
		err := tx.QueryRowContext(
			ctx,
			`SELECT `+fallbackRowColumns+` FROM businesses
			WHERE businesses.id = ? AND businesses.deleted_at IS NULL`,
			id,
		).Scan(
			&reloaded.id,
			&reloaded.name,
			&reloaded.normalizedName,
			&reloaded.normalizedAddress,
			&reloaded.city,
			&reloaded.postalCode,
			&reloaded.placeID,
			&reloaded.cid,
			&reloaded.dataID,
			&reloaded.latitude,
			&reloaded.longitude,
			&reloaded.mergedInto,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("follow merged business: %w", err)
		}
		if reloaded.mergedInto == "" {
			return reloaded, nil
		}
		id = reloaded.mergedInto
	}

	return nil, nil
}

// scoreFallbackRows applies the corroboration tiers and splits the outcome
// into at most one automatic attach plus review candidates.
func scoreFallbackRows(
	ctx context.Context,
	tx *sql.Tx,
	business resultimport.Business,
	rows []*fallbackRow,
) (identityResolution, error) {
	resolution := identityResolution{}

	type scoredRow struct {
		row        *fallbackRow
		method     string
		confidence float64
		evidence   []identityEvidence
		attachable bool
	}
	scored := make([]scoredRow, 0, len(rows))

	for _, row := range rows {
		method, confidence, evidence, chain := scoreFallbackRow(business, row)
		if confidence < identityCandidateConfidence {
			continue
		}
		suppressed, err := keepSeparatePairExists(ctx, tx, row.id, business.ID)
		if err != nil {
			return identityResolution{}, err
		}
		if suppressed {
			// A human already ruled this pair as distinct businesses: never
			// attach and never re-file the suggestion.
			continue
		}
		attachable := false
		switch {
		case chain:
			// Multi-location pattern: shared contact point, distant and
			// different addresses. Never auto-merge; only near-identical
			// names without any place_id involvement warrant review.
			nameSimilarity := resultimport.NameSimilarity(business.Name, row.name)
			if nameSimilarity < identityChainNameSimilarity || business.PlaceID != "" || row.placeID != "" {
				continue
			}
		case confidence >= identityDriftConfidence:
			// The drift tier may absorb a reassigned or first-seen Google
			// identifier, but a disagreement between two identifiers of the
			// same kind still needs the pair to describe one location under
			// one name; otherwise these are two listings, not one drift.
			attachable = !authoritativeConflict(business, row) ||
				identityDriftPlausible(business, row)
		case confidence >= identityAttachConfidence:
			attachable = !authoritativeConflict(business, row)
		}
		scored = append(scored, scoredRow{
			row:        row,
			method:     method,
			confidence: confidence,
			evidence:   evidence,
			attachable: attachable,
		})
	}

	bestIndex := -1
	for index, entry := range scored {
		if !entry.attachable {
			continue
		}
		if bestIndex < 0 || entry.confidence > scored[bestIndex].confidence {
			bestIndex = index
		}
	}

	if bestIndex >= 0 {
		best := scored[bestIndex]
		resolution.businessID = best.row.id
		resolution.method = best.method
		resolution.confidence = best.confidence
		resolution.evidence = best.evidence
		resolution.drift, resolution.driftBefore, resolution.driftAfter = detectIdentityDrift(business, best.row)
		if resolution.drift {
			resolution.evidence = append(resolution.evidence, identityEvidence{
				Signal: "identity_keys_added",
				Value:  strings.Join(resolution.driftAfter, " "),
				Detail: "authoritative keys recorded from this observation",
			})
		}
	}

	for index, entry := range scored {
		if index == bestIndex {
			continue
		}
		resolution.candidates = append(resolution.candidates, identityCandidate{
			businessID: entry.row.id,
			score:      entry.confidence,
			evidence:   entry.evidence,
		})
	}

	return resolution, nil
}

// scoreFallbackRow evaluates one stored business against the incoming record.
// chain reports the multi-location pattern that forbids automatic attaches.
func scoreFallbackRow(
	business resultimport.Business,
	row *fallbackRow,
) (string, float64, []identityEvidence, bool) {
	nameSimilarity := resultimport.NameSimilarity(business.Name, row.name)
	distance, hasDistance := fallbackDistanceMetres(business, row)

	evidence := make([]identityEvidence, 0, 6)
	appendSignal := func(signal, value, detail string) {
		evidence = append(evidence, identityEvidence{Signal: signal, Value: value, Detail: detail})
	}
	if row.phoneMatch {
		appendSignal("phone", row.phoneValue, "shared phone number")
	}
	if row.domainMatch {
		appendSignal("domain", row.domainValue, "shared website domain")
	}
	if row.addressMatch {
		appendSignal("address", row.addressValue, "same normalized address")
	}
	appendSignal("name_similarity", strconv.FormatFloat(nameSimilarity, 'f', 3, 64), "")
	if hasDistance {
		appendSignal("distance_m", strconv.FormatFloat(distance, 'f', 0, 64), "")
	}

	differentAddresses := business.Address.Normalized != "" && row.normalizedAddress != "" &&
		business.Address.Normalized != row.normalizedAddress
	chain := (row.phoneMatch || row.domainMatch) && differentAddresses &&
		separateLocations(
			distance, hasDistance,
			business.Address.City, row.city,
			business.Address.PostalCode, row.postalCode,
		)
	if chain {
		appendSignal("multi_location_pattern", "",
			"shared contact point with distant, different addresses")
	}

	method := ""
	confidence := 0.0
	consider := func(candidateMethod string, candidateConfidence float64) {
		if candidateConfidence > confidence {
			method = candidateMethod
			confidence = candidateConfidence
		}
	}
	if row.phoneMatch &&
		(nameSimilarity >= identityPhoneNameSimilarity ||
			(hasDistance && distance <= identityPhoneDistanceMetres &&
				nameSimilarity >= identityPhoneUnrelatedNameFloor)) {
		consider(identityMethodPhone, identityPhoneConfidence)
	}
	if row.domainMatch && hasDistance && distance <= identityDomainDistanceMetres &&
		nameSimilarity >= identityDomainNameSimilarity {
		consider(identityMethodDomain, identityDomainConfidence)
	}
	if row.addressMatch && nameSimilarity >= identityAddressNameSimilarity {
		consider(identityMethodAddressName, identityAddressConfidence)
	}
	if nameSimilarity >= identityNameSimilarityFloor && hasDistance &&
		distance <= identityNameDistanceMetres {
		consider(identityMethodNameProximity, identityNameConfidence)
	}

	if chain && confidence < identityCandidateConfidence {
		// A pure multi-location echo still deserves a review entry when the
		// names are near-identical; scoreFallbackRows applies that gate.
		confidence = identityCandidateConfidence
	}

	return method, confidence, evidence, chain
}

func fallbackDistanceMetres(business resultimport.Business, row *fallbackRow) (float64, bool) {
	if business.Latitude == nil || business.Longitude == nil ||
		!row.latitude.Valid || !row.longitude.Valid {
		return 0, false
	}

	return geographicDistanceMeters(
		*business.Latitude,
		*business.Longitude,
		row.latitude.Float64,
		row.longitude.Float64,
	), true
}

// separateLocations reports that two records sit at demonstrably different
// places. Coordinates decide it when both sides have them; otherwise a
// disagreeing city or postal code is the only remaining evidence, and it is
// enough, because a business that merely moved keeps its locality far more
// often than a chain shares one. Missing data is never treated as distance,
// so a rediscovery with no geography still attaches.
func separateLocations(
	distance float64,
	hasDistance bool,
	incomingCity, storedCity string,
	incomingPostalCode, storedPostalCode string,
) bool {
	if hasDistance {
		return distance > identityChainDistanceMetres
	}
	if differentLocalityValue(incomingCity, storedCity) {
		return true
	}

	return differentLocalityValue(incomingPostalCode, storedPostalCode)
}

// differentLocalityValue compares one locality field, treating a missing value
// on either side as "unknown" rather than "different".
func differentLocalityValue(incoming, stored string) bool {
	incoming = resultimport.NormalizeAddress(incoming)
	stored = resultimport.NormalizeAddress(stored)
	if incoming == "" || stored == "" {
		return false
	}

	return incoming != stored
}

// identityDriftPlausible reports whether a corroborated match may still attach
// despite disagreeing Google identifiers. Google reassigning a place_id does
// not rename or relocate the business, so the two records must agree on the
// street address (or one of them must not state one) and carry near-identical
// names. Anything looser folds a neighbouring listing into its neighbour.
func identityDriftPlausible(business resultimport.Business, row *fallbackRow) bool {
	if business.Address.Normalized != "" && row.normalizedAddress != "" &&
		business.Address.Normalized != row.normalizedAddress {
		return false
	}

	return resultimport.NameSimilarity(business.Name, row.name) >= identityDriftNameSimilarity
}

// authoritativeConflict reports whether both sides carry the same kind of
// Google identifier with different values. Such records never auto-merge on
// corroborated evidence below the drift tier.
func authoritativeConflict(business resultimport.Business, row *fallbackRow) bool {
	pairs := [][2]string{
		{business.PlaceID, row.placeID},
		{business.CID, row.cid},
		{business.DataID, row.dataID},
	}
	for _, pair := range pairs {
		if pair[0] != "" && pair[1] != "" && pair[0] != pair[1] {
			return true
		}
	}

	return false
}

// detectIdentityDrift reports authoritative keys on the incoming record that
// the attach target lacks or contradicts, with before/after key lists for the
// audit trail.
func detectIdentityDrift(
	business resultimport.Business,
	row *fallbackRow,
) (bool, []string, []string) {
	before := make([]string, 0, 3)
	after := make([]string, 0, 3)
	drift := false
	pairs := []struct {
		kind     resultimport.IdentityKind
		incoming string
		existing string
	}{
		{kind: resultimport.IdentityPlaceID, incoming: business.PlaceID, existing: row.placeID},
		{kind: resultimport.IdentityCID, incoming: business.CID, existing: row.cid},
		{kind: resultimport.IdentityDataID, incoming: business.DataID, existing: row.dataID},
	}
	for _, pair := range pairs {
		if pair.existing != "" {
			before = append(before, string(pair.kind)+":"+pair.existing)
		}
		if pair.incoming == "" {
			continue
		}
		after = append(after, string(pair.kind)+":"+pair.incoming)
		if pair.incoming != pair.existing {
			drift = true
		}
	}

	return drift, before, after
}

// keepSeparatePairExists reports a permanent human "these are different
// businesses" decision for the pair, in either orientation.
func keepSeparatePairExists(ctx context.Context, tx *sql.Tx, leftID, rightID string) (bool, error) {
	if leftID == "" || rightID == "" || leftID == rightID {
		return false, nil
	}
	var one int
	err := tx.QueryRowContext(
		ctx,
		`SELECT 1 FROM dedup_rules
		WHERE rule_type = 'business_pair' AND action = 'keep_separate'
			AND ((left_key = ? AND right_key = ?) OR (left_key = ? AND right_key = ?))
		LIMIT 1`,
		leftID,
		rightID,
		rightID,
		leftID,
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check dedup non-match rule: %w", err)
	}

	return true, nil
}

// applyIdentityOutcome persists identity provenance and the place-ID drift
// audit entry for one imported observation. Exact attaches never rewrite
// provenance, so a re-import that resolves through recorded keys is a no-op.
func applyIdentityOutcome(
	ctx context.Context,
	tx *sql.Tx,
	targetID string,
	business resultimport.Business,
	resolution identityResolution,
	wasNew bool,
	observedAt int64,
) error {
	switch {
	case wasNew:
		method, confidence, evidence := newRowIdentityProvenance(business)

		return writeIdentityProvenance(ctx, tx, targetID, method, confidence, evidence, observedAt)
	case resolution.businessID != "" && resolution.method != identityMethodExact &&
		resolution.businessID != business.ID:
		// A corroborated attach onto a different stored row records how the
		// two observations were linked. Re-attaching a record to its own row
		// (a re-import under a new job) leaves provenance untouched.
		if err := writeIdentityProvenance(
			ctx, tx, targetID,
			resolution.method, resolution.confidence, resolution.evidence,
			observedAt,
		); err != nil {
			return err
		}
		if resolution.drift {
			return recordIdentityDrift(ctx, tx, targetID, resolution, observedAt)
		}

		return nil
	default:
		return nil
	}
}

// newRowIdentityProvenance describes how a brand-new business row was
// identified: authoritative keys, weaker contact keys, or content hash only.
func newRowIdentityProvenance(business resultimport.Business) (string, float64, []identityEvidence) {
	authoritative := authoritativeIdentityKeys(business)
	evidence := make([]identityEvidence, 0, len(business.IdentityKeys))
	for _, key := range business.IdentityKeys {
		evidence = append(evidence, identityEvidence{Signal: string(key.Kind), Value: key.Value})
	}
	switch {
	case len(authoritative) > 0:
		return identityMethodNew, 1.0, evidence
	case len(business.IdentityKeys) > 0:
		return identityMethodNew, 0.7, evidence
	default:
		return identityMethodNew, 0.5, []identityEvidence{{
			Signal: "content_hash",
			Value:  business.CanonicalIdentityKey,
			Detail: "no identity signals on the source row",
		}}
	}
}

func writeIdentityProvenance(
	ctx context.Context,
	tx *sql.Tx,
	businessID string,
	method string,
	confidence float64,
	evidence []identityEvidence,
	observedAt int64,
) error {
	if evidence == nil {
		evidence = []identityEvidence{}
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE businesses SET
			identity_method = ?,
			identity_confidence = ?,
			identity_evidence = ?,
			updated_at = MAX(updated_at, ?)
		WHERE id = ?`,
		method,
		confidence,
		mustJSON(evidence, "[]"),
		observedAt,
		businessID,
	); err != nil {
		return fmt.Errorf("write identity provenance: %w", err)
	}

	return nil
}

// recordIdentityDrift files the audit entry for authoritative keys absorbed
// through a corroborated attach, mirroring the record-level change shape.
func recordIdentityDrift(
	ctx context.Context,
	tx *sql.Tx,
	businessID string,
	resolution identityResolution,
	observedAt int64,
) error {
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO business_changes(
			business_id, from_version_id, to_version_id, field_name,
			before_value, after_value, change_kind, detected_at
		) VALUES (?, NULL, NULL, 'identity_keys', ?, ?, 'identity_drift', ?)`,
		businessID,
		mustJSON(resolution.driftBefore, "[]"),
		mustJSON(resolution.driftAfter, "[]"),
		observedAt,
	); err != nil {
		return fmt.Errorf("record identity drift: %w", err)
	}

	return nil
}

// storeFallbackCandidates files uncertain matches as pending review pairs.
// INSERT OR IGNORE keeps re-imports from double-filing, and pairs a human
// ruled as distinct are suppressed permanently.
func storeFallbackCandidates(
	ctx context.Context,
	tx *sql.Tx,
	targetID string,
	candidates []identityCandidate,
	observedAt int64,
) error {
	for _, candidate := range candidates {
		if candidate.businessID == targetID {
			continue
		}
		suppressed, err := keepSeparatePairExists(ctx, tx, targetID, candidate.businessID)
		if err != nil {
			return err
		}
		if suppressed {
			continue
		}
		left, right := targetID, candidate.businessID
		if left > right {
			left, right = right, left
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT OR IGNORE INTO duplicate_candidates(
				left_business_id, right_business_id, score, signals, state, created_at
			) VALUES (?, ?, ?, ?, 'pending', ?)`,
			left,
			right,
			candidate.score,
			mustJSON(candidate.evidence, "[]"),
			observedAt,
		); err != nil {
			return fmt.Errorf("store fallback duplicate candidate: %w", err)
		}
	}

	return nil
}
