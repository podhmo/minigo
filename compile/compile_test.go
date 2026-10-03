package compile

import "testing"

// fresh must never mint the same name twice — a hoisted $argN colliding
// with a still-live sibling slot was difffuzz #118; routing every
// synthesized $-name through one sequence removes the whole class.
func TestFreshUnique(t *testing.T) {
	c := &compiler{}
	seen := map[string]bool{}
	for _, base := range []string{"$arg", "$sel", "$it", "$tag", "$tsubj", "$recv"} {
		for i := 0; i < 3; i++ {
			n := c.fresh(base)
			if seen[n] {
				t.Fatalf("fresh(%q) returned duplicate %q", base, n)
			}
			seen[n] = true
		}
	}
	a, b := c.fresh("$arg"), c.fresh("$arg")
	if a == b {
		t.Fatalf("consecutive fresh() calls must differ, got %q", a)
	}
}
