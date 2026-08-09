// Package fracindex validates fractional-index order keys — the z-order keys
// the whiteboard backend expects on every Excalidraw element.
//
// A key is accepted only if it satisfies every rule the frontend and backend
// enforce, so the CLI rejects locally exactly what they reject server-side:
//   - a well-formed integer part (correct length for its head character), from
//     the reference `fractional-indexing` library's validateOrderKey;
//   - a full-key base-62 charset, from the frontend/backend INDEX_RE
//     (/^[A-Za-z0-9]+$/) — the integer part is charset-checked, not just the
//     fraction;
//   - no trailing '0' in the fractional part, from the reference
//     validateOrderKey (a non-canonical key).
//
// The CLI uses it to reject malformed keys locally, before they reach the
// backend, so callers can no longer send index-less or garbage-index elements
// (e.g. the "r00000003" repair artifact from XIN-792) that corrupt a board.
package fracindex

import (
	"errors"
	"fmt"
)

// base62Digits is the ordered digit alphabet used for the fractional part of a
// key. The integer part's magnitude is encoded in the head character.
const base62Digits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// smallestInteger is the reserved lower bound the library never emits as a real
// key; treating it as valid would let a caller pin an element below every other.
const smallestInteger = "A00000000000000000000000000"

// integerLength returns the total length the integer part must have given its
// head character. Mirrors the reference getIntegerLength: 'a'..'z' encode
// increasing positive magnitudes, 'A'..'Z' increasing negative magnitudes.
func integerLength(head byte) (int, error) {
	switch {
	case head >= 'a' && head <= 'z':
		return int(head-'a') + 2, nil
	case head >= 'A' && head <= 'Z':
		return int('Z'-head) + 2, nil
	default:
		return 0, fmt.Errorf("invalid order key head: %q", string(head))
	}
}

// ValidateOrderKey reports whether key is a well-formed fractional index. It
// returns nil for a valid key and a descriptive error otherwise. An empty key
// is always invalid.
func ValidateOrderKey(key string) error {
	if key == "" {
		return errors.New("order key is empty")
	}
	if key == smallestInteger {
		return fmt.Errorf("invalid order key: %q (reserved lower bound)", key)
	}
	intLen, err := integerLength(key[0])
	if err != nil {
		return err
	}
	if intLen > len(key) {
		return fmt.Errorf("invalid order key: %q (integer part truncated)", key)
	}
	// Every byte of the key — integer part and fractional part alike — must be a
	// base-62 digit. The frontend (INDEX_RE = /^[A-Za-z0-9]+$/) and backend
	// (identical regex) apply this gate to the whole key, so a non-base62
	// integer-part byte such as the '!' in "a!" must be rejected here too.
	for i := 0; i < len(key); i++ {
		if !isBase62(key[i]) {
			return fmt.Errorf("invalid order key: %q (bad digit %q)", key, string(key[i]))
		}
	}
	// The fractional part (everything after the integer part) must not end in the
	// first digit '0': a trailing zero is non-canonical and equivalent to the
	// shorter key, so the reference validateOrderKey and the frontend/backend all
	// reject it (e.g. "a00", "a10").
	if len(key) > intLen && key[len(key)-1] == base62Digits[0] {
		return fmt.Errorf("invalid order key: %q (fractional part ends in zero)", key)
	}
	return nil
}

func isBase62(c byte) bool {
	for i := 0; i < len(base62Digits); i++ {
		if base62Digits[i] == c {
			return true
		}
	}
	return false
}

// GenerateKeyBetween returns a canonical fractional index strictly between a
// and b. Nil denotes an open bound. Its behavior matches fractional-indexing v4.
//
// The generated key is validated as a hard postcondition: it must itself be a
// canonical order key strictly inside the (normalized) bounds. An out-of-range
// or non-canonical candidate is returned as an error, never silently repaired
// or reversed.
func GenerateKeyBetween(a, b *string) (string, error) {
	if a != nil {
		if err := ValidateOrderKey(*a); err != nil {
			return "", fmt.Errorf("invalid lower bound: %w", err)
		}
	}
	if b != nil {
		if err := ValidateOrderKey(*b); err != nil {
			return "", fmt.Errorf("invalid upper bound: %w", err)
		}
	}
	// fractional-indexing v4 accepts closed bounds in either order as a
	// convenience, normalizing them before generation.
	if a != nil && b != nil && *a > *b {
		a, b = b, a
	} else if a != nil && b != nil && *a == *b {
		return "", errors.New("bounds must be distinct")
	}

	key, err := generateBetween(a, b)
	if err != nil {
		return "", err
	}
	if err := assertStrictlyBetween(key, a, b); err != nil {
		return "", err
	}
	return key, nil
}

// generateBetween is the core v4 generation, assuming a and b are already
// validated and normalized (a < b when both are set).
func generateBetween(a, b *string) (string, error) {
	if a == nil {
		if b == nil {
			return "a0", nil
		}
		ib, fb := splitKey(*b)
		if ib == smallestInteger {
			return ib + midpoint("", fb), nil
		}
		// When b has a fractional part, its integer part is already a valid,
		// particularly short key below it. v4 returns it before decrementing.
		if ib < *b {
			return ib, nil
		}
		dec := decrementInteger(ib)
		if dec == "" {
			return "", errors.New("cannot decrement any more")
		}
		// decrementInteger can land on the reserved smallestInteger — this
		// happens exactly when b is its immediate successor (e.g.
		// "A00000000000000000000000001"). smallestInteger is not a valid key, so
		// descend into a nonzero fraction on it instead. That fraction sorts
		// below b (whose fractional part is empty) yet remains a canonical key.
		if dec == smallestInteger {
			return smallestInteger + midpoint("", ""), nil
		}
		return dec, nil
	}
	ia, fa := splitKey(*a)
	if b == nil {
		if inc := incrementInteger(ia); inc != "" {
			return inc, nil
		}
		return ia + midpoint(fa, ""), nil
	}
	ib, fb := splitKey(*b)
	if ia == ib {
		return ia + midpoint(fa, fb), nil
	}
	inc := incrementInteger(ia)
	if inc == "" {
		return "", errors.New("cannot increment any more")
	}
	if inc < *b {
		return inc, nil
	}
	return ia + midpoint(fa, ""), nil
}

// assertStrictlyBetween enforces the generation postcondition: key must be a
// canonical order key and lie strictly between the (already normalized) bounds.
func assertStrictlyBetween(key string, a, b *string) error {
	if err := ValidateOrderKey(key); err != nil {
		return fmt.Errorf("generated non-canonical key %q: %w", key, err)
	}
	if a != nil && key <= *a {
		return fmt.Errorf("generated key %q is not above lower bound %q", key, *a)
	}
	if b != nil && key >= *b {
		return fmt.Errorf("generated key %q is not below upper bound %q", key, *b)
	}
	return nil
}

// GenerateNKeysBetween returns n sorted, evenly distributed keys between the
// open or closed bounds. Divide-and-conquer avoids progressively deep keys.
//
// Bounds are validated and normalized before any generation, for every n
// (including zero): a malformed bound is rejected, equal closed bounds are
// rejected consistently regardless of n, and reversed closed bounds are swapped
// into ascending order so the divide-and-conquer recursion — which assumes
// a < b — never sees a descending pair (which otherwise yields a descending run
// that fails the postcondition below). n < 0 remains an error.
//
// The whole emitted sequence is then validated as a hard postcondition: every
// key is canonical, the run is strictly increasing, and it stays within the
// (normalized) bounds. A violation is returned as an error rather than silently
// repaired.
func GenerateNKeysBetween(a, b *string, n int) ([]string, error) {
	if n < 0 {
		return nil, errors.New("key count must be non-negative")
	}
	// Validate + normalize bounds up front for every n, so an equal/malformed
	// bound is rejected even when n == 0 (which generates nothing) and a reversed
	// pair is corrected before it can reach the recursion.
	if a != nil {
		if err := ValidateOrderKey(*a); err != nil {
			return nil, fmt.Errorf("invalid lower bound: %w", err)
		}
	}
	if b != nil {
		if err := ValidateOrderKey(*b); err != nil {
			return nil, fmt.Errorf("invalid upper bound: %w", err)
		}
	}
	if a != nil && b != nil {
		if *a == *b {
			return nil, errors.New("bounds must be distinct")
		}
		if *a > *b {
			a, b = b, a
		}
	}
	if n == 0 {
		return []string{}, nil
	}
	keys, err := generateNKeysBetween(a, b, n)
	if err != nil {
		return nil, err
	}
	prev := a
	for _, k := range keys {
		if err := assertStrictlyBetween(k, prev, b); err != nil {
			return nil, err
		}
		kk := k
		prev = &kk
	}
	return keys, nil
}

// generateNKeysBetween is the divide-and-conquer core. Its bounds are already
// validated and normalized (a < b when both are set) by GenerateNKeysBetween;
// the n <= 0 guards remain as recursion base cases (a split can request zero
// keys on one side).
func generateNKeysBetween(a, b *string, n int) ([]string, error) {
	if n < 0 {
		return nil, errors.New("key count must be non-negative")
	}
	if n == 0 {
		return []string{}, nil
	}
	if n == 1 {
		key, err := GenerateKeyBetween(a, b)
		if err != nil {
			return nil, err
		}
		return []string{key}, nil
	}
	if b == nil {
		out := make([]string, n)
		lower := a
		for i := range out {
			key, err := GenerateKeyBetween(lower, nil)
			if err != nil {
				return nil, err
			}
			out[i], lower = key, &out[i]
		}
		return out, nil
	}
	if a == nil {
		out := make([]string, n)
		upper := b
		for i := n - 1; i >= 0; i-- {
			key, err := GenerateKeyBetween(nil, upper)
			if err != nil {
				return nil, err
			}
			out[i], upper = key, &out[i]
		}
		return out, nil
	}
	mid, err := GenerateKeyBetween(a, b)
	if err != nil {
		return nil, err
	}
	leftN := n / 2
	left, err := generateNKeysBetween(a, &mid, leftN)
	if err != nil {
		return nil, err
	}
	right, err := generateNKeysBetween(&mid, b, n-leftN-1)
	if err != nil {
		return nil, err
	}
	return append(append(left, mid), right...), nil
}

func splitKey(key string) (string, string) {
	n, _ := integerLength(key[0])
	return key[:n], key[n:]
}

func midpoint(a, b string) string {
	if b != "" && a >= b {
		panic("midpoint bounds are not ordered")
	}
	if (a != "" && a[len(a)-1] == base62Digits[0]) ||
		(b != "" && b[len(b)-1] == base62Digits[0]) {
		panic("midpoint bound has trailing zero")
	}
	if b != "" {
		// Match v4's padded common-prefix walk: missing digits in a compare as
		// zero while b cannot end during the walk.
		n := 0
		for n < len(b) {
			ac := base62Digits[0]
			if n < len(a) {
				ac = a[n]
			}
			if ac != b[n] {
				break
			}
			n++
		}
		if n > 0 {
			return b[:n] + midpoint(dropPrefix(a, n), b[n:])
		}
	}
	digitA := 0
	if a != "" {
		digitA = digitIndex(a[0])
	}
	digitB := len(base62Digits)
	if b != "" {
		digitB = digitIndex(b[0])
	}
	if digitB-digitA > 1 {
		// JavaScript Math.round(0.5*(a+b)) for non-negative integers.
		return string(base62Digits[(digitA+digitB+1)/2])
	}
	if b != "" && len(b) > 1 {
		return string(b[0])
	}
	return string(base62Digits[digitA]) + midpoint(dropFirst(a), "")
}

func dropFirst(s string) string {
	if s == "" {
		return ""
	}
	return s[1:]
}
func dropPrefix(s string, n int) string {
	if n >= len(s) {
		return ""
	}
	return s[n:]
}
func digitIndex(c byte) int {
	for i := range base62Digits {
		if base62Digits[i] == c {
			return i
		}
	}
	return -1
}

func incrementInteger(x string) string {
	head := x[0]
	trailing := ""
	for i := len(x) - 1; i >= 1; i-- {
		d := digitIndex(x[i]) + 1
		if d == len(base62Digits) {
			trailing = string(base62Digits[0]) + trailing
		} else {
			return string(head) + x[1:i] + string(base62Digits[d]) + trailing
		}
	}
	if head == 'z' {
		return ""
	}
	next := nextHead(head)
	delta := mustIntegerLength(next) - mustIntegerLength(head)
	if delta > 0 {
		trailing += string(base62Digits[0])
	} else if delta < 0 {
		trailing = trailing[1:]
	}
	return string(next) + trailing
}

func decrementInteger(x string) string {
	head := x[0]
	trailing := ""
	for i := len(x) - 1; i >= 1; i-- {
		d := digitIndex(x[i]) - 1
		if d < 0 {
			trailing = string(base62Digits[len(base62Digits)-1]) + trailing
		} else {
			return string(head) + x[1:i] + string(base62Digits[d]) + trailing
		}
	}
	if head == 'A' {
		return ""
	}
	previous := previousHead(head)
	delta := mustIntegerLength(previous) - mustIntegerLength(head)
	if delta > 0 {
		trailing += string(base62Digits[len(base62Digits)-1])
	} else if delta < 0 {
		trailing = trailing[1:]
	}
	return string(previous) + trailing
}

func nextHead(head byte) byte {
	if head == 'Z' {
		return 'a'
	}
	return head + 1
}

func previousHead(head byte) byte {
	if head == 'a' {
		return 'Z'
	}
	return head - 1
}

func mustIntegerLength(head byte) int {
	n, err := integerLength(head)
	if err != nil {
		panic(err)
	}
	return n
}
