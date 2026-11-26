package runtime

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestTrapStableCodeDefaultsToRuntime(t *testing.T) {
	if diff := cmp.Diff(TrapCodeRuntime, (&Trap{}).StableCode()); diff != "" {
		t.Errorf("zero Trap stable code (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(TrapCodeUnsupported, (&Trap{Code: TrapCodeUnsupported}).StableCode()); diff != "" {
		t.Errorf("explicit Trap stable code (-want +got):\n%s", diff)
	}
}
