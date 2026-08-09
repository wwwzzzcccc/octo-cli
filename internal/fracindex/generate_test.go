package fracindex

import (
	"reflect"
	"testing"
)

// Golden values produced by fractional-indexing@4.0.0 with its default
// BASE_62_DIGITS / BASE_52_DIGITS alphabets. Besides ordinary insertion, these
// cover fractional upper bounds, midpoint Math.round behavior, reversed bounds,
// and integer-head length transitions in both directions.
func TestGenerateKeyBetweenV4Golden(t *testing.T) {
	tests := []struct {
		name string
		a, b *string
		want string
	}{
		{"empty", nil, nil, "a0"},
		{"before first", nil, ptr("a0"), "Zz"},
		{"fractional upper returns integer", nil, ptr("a0V"), "a0"},
		{"after last", ptr("a0"), nil, "a1"},
		{"between integers", ptr("a0"), ptr("a1"), "a0V"},
		{"reversed bounds", ptr("a1"), ptr("a0"), "a0V"},
		{"deep adjacent", ptr("a0V"), ptr("a0W"), "a0VV"},
		{"round midpoint upward", ptr("a0"), ptr("a0V"), "a0G"},
		{"negative head shrink", ptr("Yzz"), ptr("Z1"), "Z0"},
		{"cross negative to positive", ptr("Zz"), nil, "a0"},
		{"ignore fraction while incrementing", ptr("a0z"), nil, "a1"},
		{"largest positive head grows", ptr("yzzzzzzzzzzzzzzzzzzzzzzzzz"), nil, "z00000000000000000000000000"},
		{"smallest-band fractional midpoint", ptr("A00000000000000000000000001"), ptr("A00000000000000000000000002"), "A00000000000000000000000001V"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := GenerateKeyBetween(tc.a, tc.b)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want v4 golden %q", got, tc.want)
			}
			assertValidGeneratedKey(t, got)
			lower, upper := normalizedBounds(tc.a, tc.b)
			if lower != nil && got <= *lower {
				t.Fatalf("%q <= lower %q", got, *lower)
			}
			if upper != nil && got >= *upper {
				t.Fatalf("%q >= upper %q", got, *upper)
			}
		})
	}
}

func TestGenerateNKeysBetweenV4Golden(t *testing.T) {
	tests := []struct {
		name string
		a, b *string
		n    int
		want []string
	}{
		{"unbounded", nil, nil, 5, []string{"a0", "a1", "a2", "a3", "a4"}},
		{"before", nil, ptr("a0"), 5, []string{"Zv", "Zw", "Zx", "Zy", "Zz"}},
		{"after", ptr("a0"), nil, 5, []string{"a1", "a2", "a3", "a4", "a5"}},
		{"bounded", ptr("a0"), ptr("a1"), 5, []string{"a08", "a0G", "a0V", "a0d", "a0l"}},
		{"head transition", ptr("Yzz"), ptr("Z1"), 5, []string{"YzzG", "YzzV", "Z0", "Z0G", "Z0V"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := GenerateNKeysBetween(tc.a, tc.b, tc.n)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %q, want v4 golden %q", got, tc.want)
			}
			assertGeneratedSequence(t, got, tc.a, tc.b)
		})
	}
}

func TestGenerateNKeysBetweenDenseSelfValidates(t *testing.T) {
	got, err := GenerateNKeysBetween(ptr("a0V"), ptr("a0W"), 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 25 {
		t.Fatalf("len=%d", len(got))
	}
	assertGeneratedSequence(t, got, ptr("a0V"), ptr("a0W"))

	if keys, err := GenerateNKeysBetween(nil, nil, 0); err != nil || len(keys) != 0 {
		t.Fatalf("zero: %v %v", keys, err)
	}
	if _, err := GenerateNKeysBetween(nil, nil, -1); err == nil {
		t.Fatal("negative n must fail")
	}
	if _, err := GenerateKeyBetween(ptr("a0"), ptr("a0")); err == nil {
		t.Fatal("equal bounds must fail")
	}
}

// smallestInteger is the reserved lower bound; it must never be emitted as a
// generated key. Kept in sync with the package constant.
const reservedSmallestInteger = "A00000000000000000000000000"

// TestGenerateKeyBetweenMinimumBandFloor pins the lower-bound edge: generating a
// key below "A…01" (the immediate successor of the reserved smallestInteger)
// used to return the reserved, non-canonical smallestInteger itself. It must
// instead return a valid canonical key formed by descending into a nonzero
// fraction on smallestInteger, strictly below the upper bound.
func TestGenerateKeyBetweenMinimumBandFloor(t *testing.T) {
	upper := "A00000000000000000000000001"
	got, err := GenerateKeyBetween(nil, ptr(upper))
	if err != nil {
		t.Fatalf("unexpected error at minimum band: %v", err)
	}
	const want = "A00000000000000000000000000V"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got == reservedSmallestInteger {
		t.Fatal("must not return the reserved smallestInteger")
	}
	assertValidGeneratedKey(t, got)
	if got >= upper {
		t.Fatalf("generated %q is not strictly below upper %q", got, upper)
	}
}

// TestGenerateNKeysBetweenMinimumBandFloor pins the same floor edge for the
// batch generator: n keys below "A…01" must all be canonical, strictly
// increasing, and below the upper bound — none may be the reserved
// smallestInteger.
func TestGenerateNKeysBetweenMinimumBandFloor(t *testing.T) {
	upper := "A00000000000000000000000001"
	tests := []struct {
		n    int
		want []string
	}{
		{1, []string{"A00000000000000000000000000V"}},
		{2, []string{"A00000000000000000000000000G", "A00000000000000000000000000V"}},
		{3, []string{"A000000000000000000000000008", "A00000000000000000000000000G", "A00000000000000000000000000V"}},
	}
	for _, tc := range tests {
		got, err := GenerateNKeysBetween(nil, ptr(upper), tc.n)
		if err != nil {
			t.Fatalf("n=%d: unexpected error: %v", tc.n, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("n=%d: got %q, want %q", tc.n, got, tc.want)
		}
		for _, k := range got {
			if k == reservedSmallestInteger {
				t.Fatalf("n=%d: emitted reserved smallestInteger", tc.n)
			}
		}
		assertGeneratedSequence(t, got, nil, ptr(upper))
	}
}

// TestGenerateNKeysBetweenReversedBoundsMatchNormalized pins requirement (3):
// reversed closed bounds must be normalized BEFORE the divide-and-conquer
// recursion for n > 1, not merely at the final validation pass. Previously the
// recursion saw the descending pair and produced a descending run that failed
// the postcondition; now reversed input yields exactly the same ascending
// sequence as the normalized input.
func TestGenerateNKeysBetweenReversedBoundsMatchNormalized(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		n    int
	}{
		{"a0", "a1", 5},
		{"a0V", "a0W", 7},
		{"Yzz", "Z1", 5},
		{"a0", "a1", 2},
		{"a0", "a1", 3},
	} {
		forward, err := GenerateNKeysBetween(ptr(tc.a), ptr(tc.b), tc.n)
		if err != nil {
			t.Fatalf("forward (%s,%s,%d): %v", tc.a, tc.b, tc.n, err)
		}
		reversed, err := GenerateNKeysBetween(ptr(tc.b), ptr(tc.a), tc.n)
		if err != nil {
			t.Fatalf("reversed (%s,%s,%d): %v", tc.b, tc.a, tc.n, err)
		}
		if !reflect.DeepEqual(forward, reversed) {
			t.Fatalf("reversed %q != forward %q for (%s,%s,%d)", reversed, forward, tc.a, tc.b, tc.n)
		}
		assertGeneratedSequence(t, reversed, ptr(tc.b), ptr(tc.a))
	}
}

// TestGenerateNKeysBetweenZeroValidatesBounds pins requirement (3): n == 0 still
// validates and normalizes its bounds. A distinct valid pair yields an empty
// slice; equal bounds and malformed bounds are rejected consistently — the same
// as they are for n >= 1 — rather than silently returning [].
func TestGenerateNKeysBetweenZeroValidatesBounds(t *testing.T) {
	if keys, err := GenerateNKeysBetween(ptr("a0"), ptr("a1"), 0); err != nil || len(keys) != 0 {
		t.Fatalf("distinct valid bounds n=0: keys=%v err=%v", keys, err)
	}
	if keys, err := GenerateNKeysBetween(ptr("a1"), ptr("a0"), 0); err != nil || len(keys) != 0 {
		t.Fatalf("reversed distinct bounds n=0 must normalize to empty: keys=%v err=%v", keys, err)
	}
	if _, err := GenerateNKeysBetween(ptr("a0"), ptr("a0"), 0); err == nil {
		t.Fatal("equal bounds n=0 must fail")
	}
	if _, err := GenerateNKeysBetween(ptr("a1"), ptr("a1"), 5); err == nil {
		t.Fatal("equal bounds n>1 must fail (consistent with n=0)")
	}
	for _, bad := range []string{"", "a10", "a!", reservedSmallestInteger} {
		if _, err := GenerateNKeysBetween(ptr(bad), ptr("z0"), 0); err == nil {
			t.Fatalf("malformed lower bound %q n=0 must fail", bad)
		}
		if _, err := GenerateNKeysBetween(ptr("Zz"), ptr(bad), 0); err == nil {
			t.Fatalf("malformed upper bound %q n=0 must fail", bad)
		}
	}
}

func assertValidGeneratedKey(t *testing.T, key string) {
	t.Helper()
	if err := ValidateOrderKey(key); err != nil {
		t.Fatalf("generated non-canonical key %q: %v", key, err)
	}
}

func assertGeneratedSequence(t *testing.T, keys []string, a, b *string) {
	t.Helper()
	lower, upper := normalizedBounds(a, b)
	previous := lower
	for _, key := range keys {
		assertValidGeneratedKey(t, key)
		if previous != nil && key <= *previous {
			t.Fatalf("keys not strictly increasing: %q after %q", key, *previous)
		}
		if upper != nil && key >= *upper {
			t.Fatalf("key %q is not below upper %q", key, *upper)
		}
		k := key
		previous = &k
	}
}

func normalizedBounds(a, b *string) (*string, *string) {
	if a != nil && b != nil && *a > *b {
		return b, a
	}
	return a, b
}

func ptr(s string) *string { return &s }
