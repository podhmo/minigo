package compile

import (
	"go/constant"
	"go/parser"
	"testing"
)

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

func TestConstExprValueMinKeepsFloatDomain(t *testing.T) {
	e, err := parser.ParseExpr("int(min(float32(4), 3) / 2 * 2)")
	if err != nil {
		t.Fatal(err)
	}
	cv, ok := ConstExprValue(nil, nil, e)
	if !ok {
		t.Fatal("min expression did not fold")
	}
	n, ok := constant.Int64Val(cv)
	if !ok || n != 3 {
		t.Fatalf("min expression = %v, want 3", cv)
	}
}

func TestScopeConstShiftTypeFollowsLeftOperand(t *testing.T) {
	e, err := parser.ParseExpr("1 << uint(2)")
	if err != nil {
		t.Fatal(err)
	}
	c := &compiler{fs: newFScope(nil), iotaVal: -1}
	c.fs.pushBlock()
	cv, typed, ok := c.scopeConstVal(e, 0, -1)
	if !ok {
		t.Fatal("shift did not fold")
	}
	if typed {
		t.Fatalf("1 << uint(2) marked typed: %v", cv)
	}
}
