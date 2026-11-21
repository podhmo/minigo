package minigo

// hostEnv: the adapter wiring the engine's host environment (virtual
// cwd, path policy, argv, output, method-set oracle) to the narrow
// bind.Env the intrinsic closures depend on.

import (
	"io"

	"github.com/podhmo/minigo/runtime"
)

// bindEnv adapts the engine to bind.Env for the intrinsic bindings:
// the virtual cwd and path policy, argv, output and the method-set
// oracle, without handing the closures *Engine itself.
type bindEnv struct{ e *Engine }

func (b bindEnv) FsPath(p string) (string, error) { return b.e.fsPath(p) }
func (b bindEnv) CheckPath(p string) error        { return b.e.cfg.CheckPath(p) }
func (b bindEnv) AllowedRoots() []string          { return b.e.cfg.AllowedRoots }
func (b bindEnv) Cwd() string                     { return b.e.cwd }
func (b bindEnv) SetCwd(dir string)               { b.e.cwd = dir }
func (b bindEnv) CwdAbs(p string) string          { return b.e.cwdAbs(p) }
func (b bindEnv) Args() []string                  { return b.e.args }

func (b bindEnv) Out() io.Writer {
	if b.e.out == nil {
		return io.Discard
	}
	return b.e.out
}

func (b bindEnv) MethodSetOfValue(v runtime.Value) (map[string]bool, bool, error) {
	return b.e.td.MethodSetOfValue(v)
}

// PeelAliasTd doubles as the source.EmbedEnv seam for the //go:embed
// builtin, which needs the engine's alias-peeler.
func (b bindEnv) PeelAliasTd(td *runtime.TypeDef) *runtime.TypeDef { return b.e.td.PeelAliasTd(td) }
