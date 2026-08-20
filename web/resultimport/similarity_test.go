package resultimport

import (
	"math"
	"testing"
)

func TestNameSimilarityIdenticalAndReorderedNames(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		left  string
		right string
	}{
		{name: "identical", left: "Harbor Dental", right: "Harbor Dental"},
		{name: "case and punctuation", left: "Joe's Pizza", right: "JOES PIZZA"},
		{name: "word order", left: "Pizza Joe's", right: "Joe's Pizza"},
		{name: "legal suffix", left: "Bay Smile Dental LLC", right: "Bay Smile Dental"},
		{name: "ampersand", left: "Smith & Sons", right: "Smith and Sons"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := NameSimilarity(testCase.left, testCase.right); got != 1 {
				t.Fatalf("NameSimilarity(%q, %q) = %v, want 1", testCase.left, testCase.right, got)
			}
		})
	}
}

func TestNameSimilaritySubsetNamingScoresHigh(t *testing.T) {
	t.Parallel()

	got := NameSimilarity("Joe's Pizza", "Joe's Pizza Restaurant")
	if got < 0.7 || got >= 1 {
		t.Fatalf("NameSimilarity(subset) = %v, want in [0.7, 1)", got)
	}

	extended := NameSimilarity("Harbor Dental", "Harbor Dental Care")
	if extended < 0.7 || extended >= 1 {
		t.Fatalf("NameSimilarity(extended) = %v, want in [0.7, 1)", extended)
	}
}

func TestNameSimilarityTypoScoresAboveUnrelated(t *testing.T) {
	t.Parallel()

	typo := NameSimilarity("Acme Plumbing", "Acme Plumbng")
	unrelated := NameSimilarity("Harbor Dental", "Golden Gate Auto Repair")

	if typo < 0.5 {
		t.Fatalf("NameSimilarity(typo) = %v, want >= 0.5", typo)
	}
	if unrelated >= 0.3 {
		t.Fatalf("NameSimilarity(unrelated) = %v, want < 0.3", unrelated)
	}
	if typo <= unrelated {
		t.Fatalf("typo score %v should exceed unrelated score %v", typo, unrelated)
	}
}

func TestNameSimilarityEmptyNamesScoreZero(t *testing.T) {
	t.Parallel()

	if got := NameSimilarity("", "Harbor Dental"); got != 0 {
		t.Fatalf("NameSimilarity(empty, name) = %v, want 0", got)
	}
	if got := NameSimilarity("Harbor Dental", ""); got != 0 {
		t.Fatalf("NameSimilarity(name, empty) = %v, want 0", got)
	}
	if got := NameSimilarity("&&&", "!!!"); got != 0 {
		t.Fatalf("NameSimilarity(symbols) = %v, want 0", got)
	}
}

func TestNameSimilarityIsSymmetricBoundedAndDeterministic(t *testing.T) {
	t.Parallel()

	pairs := [][2]string{
		{"Harbor Dental", "Harbor Dental Care"},
		{"Acme Plumbing Denver", "Acme Plumbing Boulder"},
		{"Blue Bottle Coffee", "Bluebird Cafe"},
		{"A", "Some Very Long Business Name With Many Tokens"},
	}
	for _, pair := range pairs {
		forward := NameSimilarity(pair[0], pair[1])
		backward := NameSimilarity(pair[1], pair[0])
		if forward != backward {
			t.Fatalf("NameSimilarity(%q, %q) = %v, reversed = %v", pair[0], pair[1], forward, backward)
		}
		if forward < 0 || forward > 1 || math.IsNaN(forward) {
			t.Fatalf("NameSimilarity(%q, %q) = %v, want in [0, 1]", pair[0], pair[1], forward)
		}
		if again := NameSimilarity(pair[0], pair[1]); again != forward {
			t.Fatalf("NameSimilarity(%q, %q) is not deterministic: %v then %v", pair[0], pair[1], forward, again)
		}
	}
}

func TestNameSimilarityBoundsPathologicalInput(t *testing.T) {
	t.Parallel()

	long := make([]byte, 0, 4096)
	for index := 0; index < 512; index++ {
		long = append(long, "abcdefgh "...)
	}
	if got := NameSimilarity(string(long), "abcdefgh"); got != 1 {
		// Repeated tokens collapse to one unique token, so the sets match.
		t.Fatalf("NameSimilarity(repeated token) = %v, want 1", got)
	}
}
