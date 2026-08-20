package resultimport

import "strings"

// Name similarity weights. The three components are complementary: the Dice
// coefficient rewards shared token mass, containment credits subset naming
// (a name that is fully contained in a longer variant), and the bounded
// edit-distance component credits typo-level closeness independent of the
// original word order.
const (
	nameSimilarityTokenWeight       = 0.5
	nameSimilarityContainmentWeight = 0.3
	nameSimilarityEditWeight        = 0.2

	// nameSimilarityRuneLimit bounds the Levenshtein computation so a single
	// pathological name cannot make similarity scoring quadratic in the input.
	nameSimilarityRuneLimit = 128
)

// NameSimilarity scores how likely two business names refer to the same
// business, in [0, 1]. Both names are first reduced with NormalizeName
// (Unicode folding, punctuation removal, legal-suffix stripping) and split
// into unique tokens.
//
// The score is the weighted sum of three components:
//
//	similarity = 0.5*dice + 0.3*containment + 0.2*edit
//
// where dice is the Sørensen–Dice coefficient over the unique token sets
// (2·|A∩B| / (|A|+|B|)), containment is |A∩B| / min(|A|, |B|) and credits
// subset naming such as "Joe's Pizza" vs "Joe's Pizza Restaurant", and edit
// is 1 − levenshtein(a, b) / max(|a|, |b|) computed on the alphabetically
// sorted token strings so word order never matters, bounded at 128 runes per
// side. Identical normalized token sets score exactly 1; names without any
// normalized content score 0. The function is symmetric and deterministic.
func NameSimilarity(left, right string) float64 {
	leftTokens := uniqueSortedTokens(NormalizeName(left))
	rightTokens := uniqueSortedTokens(NormalizeName(right))
	if len(leftTokens) == 0 || len(rightTokens) == 0 {
		return 0
	}

	shared := sharedTokenCount(leftTokens, rightTokens)
	if shared == len(leftTokens) && shared == len(rightTokens) {
		return 1
	}

	dice := 2 * float64(shared) / float64(len(leftTokens)+len(rightTokens))
	containment := float64(shared) / float64(min(len(leftTokens), len(rightTokens)))
	edit := editSimilarity(strings.Join(leftTokens, " "), strings.Join(rightTokens, " "))
	similarity := nameSimilarityTokenWeight*dice +
		nameSimilarityContainmentWeight*containment +
		nameSimilarityEditWeight*edit

	return min(1, max(0, similarity))
}

// uniqueSortedTokens splits a normalized name into its unique tokens in
// lexicographic order.
func uniqueSortedTokens(normalized string) []string {
	fields := strings.Fields(normalized)
	if len(fields) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(fields))
	tokens := make([]string, 0, len(fields))
	for _, field := range fields {
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		tokens = append(tokens, field)
	}
	sortStrings(tokens)

	return tokens
}

// sharedTokenCount counts tokens present in both sorted unique slices.
func sharedTokenCount(left, right []string) int {
	shared, leftIndex, rightIndex := 0, 0, 0
	for leftIndex < len(left) && rightIndex < len(right) {
		switch {
		case left[leftIndex] == right[rightIndex]:
			shared++
			leftIndex++
			rightIndex++
		case left[leftIndex] < right[rightIndex]:
			leftIndex++
		default:
			rightIndex++
		}
	}

	return shared
}

// editSimilarity converts a bounded Levenshtein distance into a [0, 1] score.
func editSimilarity(left, right string) float64 {
	leftRunes := boundedRunes(left)
	rightRunes := boundedRunes(right)
	longest := max(len(leftRunes), len(rightRunes))
	if longest == 0 {
		return 1
	}
	distance := levenshteinDistance(leftRunes, rightRunes)

	return 1 - float64(distance)/float64(longest)
}

func boundedRunes(value string) []rune {
	runes := []rune(value)
	if len(runes) > nameSimilarityRuneLimit {
		runes = runes[:nameSimilarityRuneLimit]
	}

	return runes
}

// levenshteinDistance is the classic two-row dynamic-programming edit
// distance over runes with unit insert, delete, and substitute costs.
func levenshteinDistance(left, right []rune) int {
	if len(left) == 0 {
		return len(right)
	}
	if len(right) == 0 {
		return len(left)
	}

	previous := make([]int, len(right)+1)
	current := make([]int, len(right)+1)
	for index := range previous {
		previous[index] = index
	}
	for leftIndex := 1; leftIndex <= len(left); leftIndex++ {
		current[0] = leftIndex
		for rightIndex := 1; rightIndex <= len(right); rightIndex++ {
			cost := 1
			if left[leftIndex-1] == right[rightIndex-1] {
				cost = 0
			}
			current[rightIndex] = min(
				previous[rightIndex]+1,
				current[rightIndex-1]+1,
				previous[rightIndex-1]+cost,
			)
		}
		previous, current = current, previous
	}

	return previous[len(right)]
}

// sortStrings is a dependency-free insertion sort; token lists are tiny.
func sortStrings(values []string) {
	for index := 1; index < len(values); index++ {
		for position := index; position > 0 && values[position] < values[position-1]; position-- {
			values[position], values[position-1] = values[position-1], values[position]
		}
	}
}
