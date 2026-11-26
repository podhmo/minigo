package runtime_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/runtime"
)

func TestDisasmTrapSpec(t *testing.T) {
	ch := &bytecode.Chunk{Consts: []any{runtime.TrapSpec{Code: runtime.TrapCodeUnsupported, Reason: "unsupported expression"}}}
	want := "chunk  (params=0 locals=0 results=0)\nconsts:\n  k0: trap MG-UNSUPPORTED \"unsupported expression\"\n"
	if diff := cmp.Diff(want, bytecode.Disasm(ch)); diff != "" {
		t.Errorf("disassembly mismatch (-want +got):\n%s", diff)
	}
}
