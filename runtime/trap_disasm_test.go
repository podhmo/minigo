package runtime_test

import (
	"strings"
	"testing"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/runtime"
)

func TestDisasmTrapSpec(t *testing.T) {
	ch := &bytecode.Chunk{Consts: []any{runtime.TrapSpec{Code: runtime.TrapCodeUnsupported, Reason: "unsupported expression"}}}
	got := bytecode.Disasm(ch)
	for _, want := range []string{"MG-UNSUPPORTED", `"unsupported expression"`} {
		if !strings.Contains(got, want) {
			t.Errorf("disassembly missing %s\n---\n%s", want, got)
		}
	}
}
