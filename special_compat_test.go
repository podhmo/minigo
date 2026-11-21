package minigo_test

import (
	"testing"

	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/special"
)

// TestSpecialAPIPlacement pins the public special-forms surface (P2-6 /
// P2-14d): SymbolID and QuotedCall live in the public special package as
// aliases from runtime (I7); SpecialFunc and SpecialContext stay in
// runtime because their signatures reference runtime values — moving them
// to special would create a runtime <-> special import cycle. Plain
// assignments, not conversions, prove the types are identical.
func TestSpecialAPIPlacement(t *testing.T) {
	var sid special.SymbolID = runtime.SymbolID{PackagePath: "p", Name: "n"}
	var _ runtime.SymbolID = sid
	var q special.QuotedCall = runtime.QuotedCall{Call: nil}
	var _ runtime.QuotedCall = q

	var fn runtime.SpecialFunc = func(ctx runtime.SpecialContext, call *runtime.QuotedCall) (runtime.Value, error) {
		return nil, nil
	}
	var _ runtime.SpecialContext
	_ = fn
}
