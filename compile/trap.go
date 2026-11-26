package compile

import (
	"fmt"
	"go/token"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/runtime"
)

func (c *compiler) trap(pos token.Pos, format string, args ...any) int {
	return c.trapCode(pos, runtime.TrapCodeRuntime, format, args...)
}

func (c *compiler) trapCode(pos token.Pos, code runtime.TrapCode, format string, args ...any) int {
	return c.emit(bytecode.OpTrap, c.constIdx(runtime.TrapSpec{Code: code, Reason: fmt.Sprintf(format, args...)}), 0, pos)
}
