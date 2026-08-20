package resultimport

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
)

const cursorPrefix = "ri1_"

func finalizeBusiness(business *Business, rawHash string) {
	business.IdentityKeys = exactIdentityKeys(*business)
	business.CanonicalIdentityKey = canonicalIdentityKey(*business, rawHash)
	business.ID = "biz_" + hashParts("business-id-v1", business.CanonicalIdentityKey)[:32]
	business.RecordHash = businessHash(*business)
}

// canonicalIdentityKey picks the value that names a business row. Only the
// authoritative Google identifiers (place_id, cid, data_id) are trusted as a
// standalone identity: a phone, domain, or address is shared across a chain's
// locations, so records carrying only those signals get a composite fallback
// key that keeps two locations with a shared contact point distinct.
func canonicalIdentityKey(business Business, rawHash string) string {
	for _, key := range business.IdentityKeys {
		switch key.Kind {
		case IdentityPlaceID, IdentityCID, IdentityDataID:
			return key.String()
		}
	}
	if len(business.IdentityKeys) > 0 {
		parts := []string{
			business.NormalizedName,
			business.NormalizedCategory,
			business.Address.Normalized,
			floatValue(business.Latitude),
			floatValue(business.Longitude),
		}
		for _, key := range business.IdentityKeys {
			parts = append(parts, key.String())
		}

		return "fallback:" + hashParts("fallback-v2", parts...)
	}

	return "fallback:" + hashParts(
		"fallback-v1",
		business.NormalizedName,
		business.NormalizedCategory,
		business.Address.Normalized,
		floatValue(business.Latitude),
		floatValue(business.Longitude),
		rawHash,
	)
}

func exactIdentityKeys(business Business) []IdentityKey {
	keys := make([]IdentityKey, 0, 6+len(business.Phones))
	appendKey := func(kind IdentityKind, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, key := range keys {
			if key.Kind == kind && key.Value == value {
				return
			}
		}
		keys = append(keys, IdentityKey{Kind: kind, Value: value})
	}

	appendKey(IdentityPlaceID, business.PlaceID)
	appendKey(IdentityCID, business.CID)
	appendKey(IdentityDataID, business.DataID)
	for _, phone := range business.Phones {
		appendKey(IdentityPhone, phone.MatchKey)
	}
	appendKey(IdentityDomain, business.Website.Domain)
	if len([]rune(business.Address.Normalized)) >= 6 {
		appendKey(IdentityAddress, business.Address.Normalized)
	}

	return keys
}

func businessHash(business Business) string {
	parts := []string{
		business.NormalizedName,
		business.NormalizedCategory,
		business.Address.Normalized,
		NormalizeAddress(business.Address.Borough),
		NormalizeAddress(business.Address.Street),
		NormalizeAddress(business.Address.City),
		business.Address.State,
		business.Address.PostalCode,
		business.Address.Country,
		business.MapsURL.Canonical,
		business.Website.Canonical,
		business.PlaceID,
		business.CID,
		business.DataID,
		strings.ToUpper(strings.TrimSpace(business.PlusCode)),
		business.Status,
		cleanDisplay(business.Description),
		business.ReviewsURL.Canonical,
		business.ThumbnailURL.Canonical,
		business.StreetViewURL.Canonical,
		strings.TrimSpace(business.Timezone),
		strings.TrimSpace(business.PriceRange),
		intValue(business.ReviewCount),
		floatValue(business.ReviewRating),
		floatValue(business.Latitude),
		floatValue(business.Longitude),
		canonicalJSON(business.Structured.OpenHours),
		canonicalJSON(business.Structured.PopularTimes),
		canonicalJSON(business.Structured.ReviewsPerRating),
		canonicalJSON(business.Structured.Images),
		canonicalJSON(business.Structured.Reservations),
		canonicalJSON(business.Structured.OrderOnline),
		canonicalJSON(business.Structured.Menu),
		canonicalJSON(business.Structured.Owner),
		canonicalJSON(business.Structured.CompleteAddress),
		canonicalJSON(business.Structured.About),
		canonicalJSON(business.Structured.UserReviews),
		canonicalJSON(business.Structured.UserReviewsExtended),
	}
	parts = append(parts, sortPhones(business.Phones)...)
	parts = append(parts, sortEmails(business.Emails)...)
	cards := append([]string(nil), business.CreditCardsAccepted...)
	for index := range cards {
		cards[index] = casesFold(cards[index])
	}
	sort.Strings(cards)
	parts = append(parts, cards...)

	return hashParts("business-record-v1", parts...)
}

func rawRecordHash(raw RawRecord) string {
	keys := make([]string, 0, len(raw.Fields))
	for key := range raw.Fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys)*2)
	for _, key := range keys {
		parts = append(parts, key, raw.Fields[key])
	}

	return hashParts("raw-record-v1", parts...)
}

func hashParts(namespace string, parts ...string) string {
	hasher := sha256.New()
	writeHashPart(hasher.Write, namespace)
	for _, part := range parts {
		writeHashPart(hasher.Write, part)
	}

	return hex.EncodeToString(hasher.Sum(nil))
}

func writeHashPart(write func([]byte) (int, error), value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = write(size[:])
	_, _ = write([]byte(value))
}

func cursorFor(sourceID, rowHash string, rowNumber int64, occurrence int) Cursor {
	token := cursorPrefix + hashParts(
		"row-cursor-v1",
		sourceID,
		rowHash,
		strconv.Itoa(occurrence),
	)

	return Cursor{
		Token:      token,
		RowNumber:  rowNumber,
		RowHash:    rowHash,
		Occurrence: occurrence,
	}
}

// ParseCursor validates an opaque cursor token for use in Options.AfterCursor.
func ParseCursor(token string) (Cursor, error) {
	if !strings.HasPrefix(token, cursorPrefix) {
		return Cursor{}, ErrInvalidCursor
	}
	digest := strings.TrimPrefix(token, cursorPrefix)
	if len(digest) != sha256.Size*2 {
		return Cursor{}, ErrInvalidCursor
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return Cursor{}, ErrInvalidCursor
	}

	return Cursor{Token: token}, nil
}

// ExactMatchKeys returns all qualified identity keys shared by two businesses.
func ExactMatchKeys(left, right Business) []IdentityKey {
	rightKeys := make(map[string]struct{}, len(right.IdentityKeys))
	for _, key := range right.IdentityKeys {
		rightKeys[key.String()] = struct{}{}
	}
	matches := make([]IdentityKey, 0)
	for _, key := range left.IdentityKeys {
		if _, ok := rightKeys[key.String()]; ok {
			matches = append(matches, key)
		}
	}
	sortIdentityKeys(matches)

	return matches
}

// IsExactDuplicate reports whether two businesses share any exact identity key.
func IsExactDuplicate(left, right Business) bool {
	return len(ExactMatchKeys(left, right)) > 0
}

// GroupExactDuplicates returns transitive duplicate groups in deterministic
// record order. Singleton records are omitted.
func GroupExactDuplicates(records []Record) []DuplicateGroup {
	if len(records) < 2 {
		return nil
	}
	parents := make([]int, len(records))
	for index := range parents {
		parents[index] = index
	}
	var find func(int) int
	find = func(value int) int {
		if parents[value] != value {
			parents[value] = find(parents[value])
		}

		return parents[value]
	}
	union := func(left, right int) {
		leftRoot := find(left)
		rightRoot := find(right)
		if leftRoot == rightRoot {
			return
		}
		if leftRoot < rightRoot {
			parents[rightRoot] = leftRoot
		} else {
			parents[leftRoot] = rightRoot
		}
	}

	owners := make(map[string]int)
	for index, record := range records {
		for _, key := range record.Business.IdentityKeys {
			qualified := key.String()
			if owner, exists := owners[qualified]; exists {
				union(index, owner)
			} else {
				owners[qualified] = index
			}
		}
	}

	members := make(map[int][]int)
	for index := range records {
		root := find(index)
		members[root] = append(members[root], index)
	}
	roots := make([]int, 0, len(members))
	for root, indexes := range members {
		if len(indexes) > 1 {
			roots = append(roots, root)
		}
	}
	sort.Ints(roots)

	groups := make([]DuplicateGroup, 0, len(roots))
	for _, root := range roots {
		indexes := members[root]
		keyCounts := make(map[string]int)
		keyValues := make(map[string]IdentityKey)
		for _, index := range indexes {
			for _, key := range records[index].Business.IdentityKeys {
				qualified := key.String()
				keyCounts[qualified]++
				keyValues[qualified] = key
			}
		}
		keys := make([]IdentityKey, 0)
		for qualified, count := range keyCounts {
			if count > 1 {
				keys = append(keys, keyValues[qualified])
			}
		}
		sortIdentityKeys(keys)
		groups = append(groups, DuplicateGroup{Records: indexes, Keys: keys})
	}

	return groups
}

func sortIdentityKeys(keys []IdentityKey) {
	sort.Slice(keys, func(left, right int) bool {
		if keys[left].Kind == keys[right].Kind {
			return keys[left].Value < keys[right].Value
		}

		return identityPriority(keys[left].Kind) < identityPriority(keys[right].Kind)
	})
}

func identityPriority(kind IdentityKind) int {
	switch kind {
	case IdentityPlaceID:
		return 0
	case IdentityCID:
		return 1
	case IdentityDataID:
		return 2
	case IdentityPhone:
		return 3
	case IdentityDomain:
		return 4
	case IdentityAddress:
		return 5
	default:
		return 6
	}
}

func intValue(value *int64) string {
	if value == nil {
		return ""
	}

	return strconv.FormatInt(*value, 10)
}

func floatValue(value *float64) string {
	if value == nil {
		return ""
	}

	return strconv.FormatFloat(*value, 'g', -1, 64)
}

func casesFold(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}
