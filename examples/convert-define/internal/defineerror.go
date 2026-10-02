package internal

import (
	"fmt"
	"go/ast"
	"go/token"

	"github.com/podhmo/minigo/runtime"
)

// DefineError is a misuse of the define DSL (an unknown field in c.Map,
// a bad define.Rule signature, ...), attributed to the define-file
// position of the offending call. The user has to fix it, and Pos says
// where.
//
// Special-form handlers return it; the VM raises it as a runtime trap
// that keeps the value (Trap.Err), so Run unwraps it and returns it —
// with the trap's DSL call frames — instead of the trap.
type DefineError struct {
	Pos    token.Position
	Msg    string
	Frames []string // DSL call frames, most recent first (from the trap)
}

func (e *DefineError) Error() string {
	return fmt.Sprintf("%s: %s", e.Pos, e.Msg)
}

// errorf builds a DefineError at n's position.
func (r *Runner) errorf(ctx runtime.SpecialContext, n ast.Node, format string, args ...any) error {
	return &DefineError{Pos: ctx.Position(n), Msg: fmt.Sprintf(format, args...)}
}
