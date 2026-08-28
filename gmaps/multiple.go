package gmaps

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	olc "github.com/google/open-location-code/go"
)

// searchPlaceIDIndex is where the Maps search response carries the stable
// ChIJ... place identifier for a listing. The same payload carries the
// hex feature id at index 10 (DataID); the CID is the decimal form of that
// id's second half. Both are strong, deterministic identities that the
// search response really does contain, so Fast mode has no reason to emit a
// row with no identity at all.
const searchPlaceIDIndex = 78

func ParseSearchResults(raw []byte) ([]*Entry, error) {
	var data []any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	if len(data) == 0 {
		return nil, fmt.Errorf("empty JSON data")
	}

	container, ok := data[0].([]any)
	if !ok || len(container) == 0 {
		return nil, fmt.Errorf("invalid business list structure")
	}

	items := getNthElementAndCast[[]any](container, 1)
	if len(items) < 2 {
		return nil, fmt.Errorf("empty business list")
	}

	entries := make([]*Entry, 0, len(items)-1)

	for i := 1; i < len(items); i++ {
		arr, ok := items[i].([]any)
		if !ok {
			continue
		}

		business := getNthElementAndCast[[]any](arr, 14)

		var entry Entry

		entry.ID = getNthElementAndCast[string](business, 0)
		entry.Title = getNthElementAndCast[string](business, 11)
		entry.Categories = toStringSlice(getNthElementAndCast[[]any](business, 13))
		entry.WebSite = getNthElementAndCast[string](business, 7, 0)

		// Google's search (tbm=map) response carries the star rating but no
		// longer carries the review count. A missing count must stay
		// missing: writing the int zero would publish "0 reviews" as a
		// captured fact about a business that may have thousands.
		if rating, ok := getNthElementAndCastOK[float64](business, 4, 7); ok {
			entry.ReviewRating = rating
		} else {
			entry.ReviewRatingUnknown = true
		}

		if count, ok := getNthElementAndCastOK[float64](business, 4, 8); ok {
			entry.ReviewCount = int(count)
		} else {
			entry.ReviewCountUnknown = true
		}

		fullAddress := getNthElementAndCast[[]any](business, 2)

		entry.Address = func() string {
			sb := strings.Builder{}

			for i, part := range fullAddress {
				if i > 0 {
					sb.WriteString(", ")
				}

				sb.WriteString(fmt.Sprintf("%v", part))
			}

			return sb.String()
		}()

		entry.Latitude = getNthElementAndCast[float64](business, 9, 2)
		entry.Longtitude = getNthElementAndCast[float64](business, 9, 3)
		entry.Phone = strings.ReplaceAll(getNthElementAndCast[string](business, 178, 0, 0), " ", "")
		entry.OpenHours = getHours(business)
		entry.Status = getNthElementAndCast[string](business, 34, 4, 4)
		entry.Timezone = getNthElementAndCast[string](business, 30)
		entry.DataID = getNthElementAndCast[string](business, 10)

		// The primary category is the first of the listed categories. It was
		// previously left empty, which blanked the export's "category"
		// column for every Fast-mode row even though the categories were
		// parsed one line above.
		if len(entry.Categories) > 0 {
			entry.Category = entry.Categories[0]
		}

		entry.PlaceID = getNthElementAndCast[string](business, searchPlaceIDIndex)
		entry.Cid = cidFromDataID(entry.DataID)
		entry.Link = mapsPlaceLink(entry.PlaceID, entry.Cid)

		entry.PlusCode = olc.Encode(entry.Latitude, entry.Longtitude, 10)

		entries = append(entries, &entry)
	}

	return entries, nil
}

// cidFromDataID converts a Maps feature id ("0x80c2c7...:0x3c94e6...") to the
// decimal CID Maps' own ?cid= links use. It returns an empty string for
// anything that is not that exact shape, so a malformed id never becomes a
// fabricated identifier.
func cidFromDataID(dataID string) string {
	_, hex, found := strings.Cut(dataID, ":")
	if !found {
		return ""
	}

	hex = strings.TrimPrefix(strings.TrimSpace(hex), "0x")
	if hex == "" {
		return ""
	}

	value, err := strconv.ParseUint(hex, 16, 64)
	if err != nil {
		return ""
	}

	return strconv.FormatUint(value, 10)
}

// mapsPlaceLink builds the canonical Maps link for a listing from the
// strongest identity available. With neither identity it returns an empty
// string rather than a link that would resolve to the wrong place.
func mapsPlaceLink(placeID, cid string) string {
	switch {
	case placeID != "":
		return "https://www.google.com/maps/place/?q=place_id:" + placeID
	case cid != "":
		return "https://maps.google.com/?cid=" + cid
	default:
		return ""
	}
}

func toStringSlice(arr []any) []string {
	ans := make([]string, 0, len(arr))
	for _, v := range arr {
		ans = append(ans, fmt.Sprintf("%v", v))
	}

	return ans
}
