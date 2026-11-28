package compile

import (
	"go/constant"
	"go/parser"
	"testing"

	"github.com/google/go-cmp/cmp"
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

func TestConstExprValueIntegerResults(t *testing.T) {
	tests := []struct {
		name string
		expr string
		want int64
	}{
		{name: "min keeps float division", expr: "int(min(float32(4), 3) / 2 * 2)", want: 3},
		{name: "typed integer division", expr: "int(int(5) / 2.0 * 2)", want: 4},
		{name: "rounded min winner", expr: "int(max(float32(0), 2.99999999))", want: 3},
		{name: "rounded min loser", expr: "int(min(float32(3), 4.1))", want: 3},
		{name: "float64 exact intermediate", expr: "int(float64(1e100) + float64(1e100) - float64(2e100) + float64(3))", want: 3},
		{name: "unsigned bitwise complement", expr: "int(^uint8(252))", want: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, err := parser.ParseExpr(tt.expr)
			if err != nil {
				t.Fatal(err)
			}
			cv, ok := ConstExprValue(nil, nil, e)
			if !ok {
				t.Fatal("constant expression did not fold")
			}
			n, ok := constant.Int64Val(cv)
			if !ok {
				t.Fatalf("constant expression = %v, want an int64 constant", cv)
			}
			if diff := cmp.Diff(tt.want, n); diff != "" {
				t.Fatalf("constant expression (-want +got):\n%s", diff)
			}
		})
	}
}

func TestScopeConstShiftTypeFollowsLeftOperand(t *testing.T) {
	e, err := parser.ParseExpr("1 << uint(2)")
	if err != nil {
		t.Fatal(err)
	}
	c := &compiler{fs: newFScope(nil), iotaVal: -1}
	c.fs.pushBlock()
	cv, typ, ok := c.scopeConstVal(e, 0, -1)
	if !ok {
		t.Fatal("shift did not fold")
	}
	if typ != "" {
		t.Fatalf("1 << uint(2) marked typed as %s: %v", typ, cv)
	}
}
