package vm

import (
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
)

func TestTrapFromConstCarriesStableCode(t *testing.T) {
	got := trapFromConst(0, runtime.TrapSpec{Code: runtime.TrapCodeUnsupported, Reason: "unsupported expression"})
	if diff := cmp.Diff(runtime.TrapCodeUnsupported, got.Code); diff != "" {
		t.Errorf("TrapSpec code (-want +got):\n%s", diff)
	}

	legacy := trapFromConst(0, "legacy trap")
	if diff := cmp.Diff(runtime.TrapCodeRuntime, legacy.Code); diff != "" {
		t.Errorf("legacy trap code (-want +got):\n%s", diff)
	}
}

func TestTrapCodeForErr(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want runtime.TrapCode
	}{
		{"undefined", fmt.Errorf("outer: %w", &runtime.UndefinedError{Reason: "undefined: x"}), runtime.TrapCodeUndefined},
		{"host conversion", fmt.Errorf("outer: %w", &hostbridge.ConversionError{Err: errors.New("bad value")}), runtime.TrapCodeHostConversion},
		{"generic", errors.New("boom"), runtime.TrapCodeRuntime},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, trapCodeForErr(tt.err)); diff != "" {
				t.Errorf("trap code (-want +got):\n%s", diff)
			}
		})
	}
}
