// Package bind names the slice of the engine's host environment the
// intrinsic bindings may observe or mutate: the virtual cwd and the
// path policy around it, the script-visible argv, the configured
// output writer and the method-set oracle. The engine adapts itself
// to Env at install time, so intrinsic closures depend on this
// surface — not on the engine — which is the seam lazy stdlib
// packages (TODO P2-12) will bind against.
package bind

import (
	"io"

	"github.com/podhmo/minigo/runtime"
)

// Env is the host environment an intrinsic binding may see: the
// engine's virtual cwd and the roots policy around it, the
// script-visible argv, the configured output writer and the
// method-set oracle. The host process's real cwd/argv/stdout are
// never exposed through it.
type Env interface {
	// FsPath maps a script path to a host path: relative paths anchor
	// at the virtual cwd and the result must pass the roots policy.
	FsPath(p string) (string, error)
	// CheckPath enforces the roots policy without anchoring — paths a
	// host call expanded itself (filepath.Glob matches) are checked
	// directly.
	CheckPath(p string) error
	// AllowedRoots is the engine's path allowlist; empty means
	// unrestricted.
	AllowedRoots() []string
	// Cwd is the script-visible working directory; SetCwd updates it
	// (os.Chdir). CwdAbs anchors a script path at it without a roots
	// check — for pure path math where no filesystem is touched.
	Cwd() string
	SetCwd(dir string)
	CwdAbs(p string) string
	// Args is the script-visible argv (os.Args); nil falls back to
	// the host process argv.
	Args() []string
	// Out is the engine's configured output writer (io.Discard when
	// unset).
	Out() io.Writer
	// MethodSetOfValue reports the method set the engine sees on a
	// script or host value.
	MethodSetOfValue(v runtime.Value) (map[string]bool, bool, error)
}
