package internal

import (
	"fmt"
	"go/ast"
	"go/token"

	"github.com/podhmo/minigo/runtime"
)

// DefineError reports a misuse of the define DSL at Pos.
// [Runner.Run] returns it with the DSL call frames in Frames.
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
