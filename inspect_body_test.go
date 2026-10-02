package minigo_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestInspectBodyScript(t *testing.T) {
	e := newEngine(t)
	for fn, want := range map[string]string{"Walk": "ok", "TypeContext": "net/http.Request"} {
		got := run(t, e, "./testdata/inspectbodyuse", fn)
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("%s (-want +got):\n%s", fn, diff)
		}
	}
	for _, fn := range []string{"HostTrap", "NonFunctionTrap", "ArityTrap"} {
		if _, err := e.Run(context.Background(), "./testdata/inspectbodyuse", fn); err == nil {
			t.Errorf("%s: expected error", fn)
		}
	}
}
