package stdos

import (
	"io"
	"os"
	"reflect"

	intbind "github.com/podhmo/minigo/internal/bind"
)

// EngineStdout is the script-visible os.Stdout: a *os.File facade whose
// writes forward to the engine's configured output so host consumers
// handed it — text/template's Execute above all — write where
// print/println write instead of escaping to the process stdout.
type EngineStdout struct {
	*os.File
	env intbind.Env
}

func (w *EngineStdout) Write(p []byte) (int, error) { return w.out().Write(p) }

func (w *EngineStdout) WriteString(s string) (int, error) {
	if sw, ok := w.out().(io.StringWriter); ok {
		return sw.WriteString(s)
	}
	return w.out().Write([]byte(s))
}

// ReadFrom keeps io.Copy(os.Stdout, r) inside the engine's output: the
// promoted (*os.File).ReadFrom would splice straight to the process's
// fd 1 and escape WithOutput the way a bare os.Stdout did.
func (w *EngineStdout) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(w.out(), r)
}

// out returns the environment's output writer (io.Discard when unset).
func (w *EngineStdout) out() io.Writer {
	if w.env == nil {
		return io.Discard
	}
	return w.env.Out()
}

// APIType spells the facade as *os.File under %T — the engine's
// internal wrapper name is not part of the script's type vocabulary.
func (w *EngineStdout) APIType() reflect.Type { return reflect.TypeFor[*os.File]() }

// UnwrapHost gives a *os.File slot the real file, like scriptFile's —
// os.Stdout as an exec.Cmd.ExtraFiles element reaches the child.
func (w *EngineStdout) UnwrapHost() any { return w.File }

// EngineFileOf unwraps the script-visible os.Stdout facade to the real
// file for net's FileConn/FilePacketConn adapters.
func EngineFileOf(v any) *os.File {
	if s, ok := v.(*EngineStdout); ok {
		return s.File
	}
	t, _ := v.(*os.File)
	return t
}
