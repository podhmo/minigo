package vm

import (
	"errors"
	"fmt"
	"go/token"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
)

func trapFromConst(pos token.Pos, v any) *runtime.Trap {
	if spec, ok := v.(runtime.TrapSpec); ok {
		return &runtime.Trap{Pos: pos, Code: spec.Code, Reason: spec.Reason}
	}
	return &runtime.Trap{Pos: pos, Code: runtime.TrapCodeRuntime, Reason: fmt.Sprint(v)}
}

func trapCodeForErr(err error) runtime.TrapCode {
	var undef *runtime.UndefinedError
	if errors.As(err, &undef) {
		return runtime.TrapCodeUndefined
	}
	var conv *hostbridge.ConversionError
	if errors.As(err, &conv) {
		return runtime.TrapCodeHostConversion
	}
	return runtime.TrapCodeRuntime
}

func trapErr(pos token.Pos, err error) *runtime.Trap {
	return &runtime.Trap{Pos: pos, Code: trapCodeForErr(err), Reason: err.Error(), Err: err}
}

// classifiedTrap preserves f.trap's historical shape (no Err cause)
// while still classifying typed errors.
func classifiedTrap(pos token.Pos, err error) *runtime.Trap {
	return &runtime.Trap{Pos: pos, Code: trapCodeForErr(err), Reason: err.Error()}
}

func (f *frame) trapCode(code runtime.TrapCode, format string, args ...any) {
	panic(&runtime.Trap{Pos: f.pos(), Code: code, Reason: fmt.Sprintf(format, args...)})
}

func (f *frame) trapErr(err error) {
	panic(classifiedTrap(f.pos(), err))
}
