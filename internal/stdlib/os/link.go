package stdos

import (
	"os"
	"path/filepath"

	intbind "github.com/podhmo/minigo/internal/bind"
	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
)

// bindLinkOps installs the hard/symbolic link surface — split out of
// Install for the file-size budget.
func bindLinkOps(ospkg map[string]runtime.Value, h stdlib.Helpers, env intbind.Env) {
	ospkg["Link"] = h.Fn2("os.Link", func(a []any) (any, error) {
		so, sn := stdlib.Str(a[0]), stdlib.Str(a[1])
		old, err := env.FsPath(so)
		if err != nil {
			return nil, err
		}
		newp, err := env.FsPath(sn)
		if err != nil {
			return nil, err
		}
		lerr := os.Link(old, newp)
		if le, ok := lerr.(*os.LinkError); ok {
			// the host error spells the resolved paths — rewrite
			// both halves to the script's spelling like
			// scriptPathErr does for *fs.PathError.
			le.Old = stdlib.ScriptPathOf(so, old, le.Old)
			le.New = stdlib.ScriptPathOf(sn, newp, le.New)
		}
		return stdlib.ErrVal(lerr), nil
	}, os.Link)
	ospkg["Symlink"] = h.Fn2("os.Symlink", func(a []any) (any, error) {
		so, sn := stdlib.Str(a[0]), stdlib.Str(a[1])
		// the link's target text is stored verbatim — resolving it
		// against the virtual cwd would write the absolute path into
		// the link and Readlink would answer that spelling (gc keeps
		// the literal oldname, which a reader resolves relative to the
		// link's directory). Only the link's own path resolves.
		newp, err := env.FsPath(sn)
		if err != nil {
			return nil, err
		}
		// a link inside an allowed root can still point outside it —
		// and a later os.ReadFile would follow it past the roots
		// policy — so the target is checked the way a reader
		// resolves it: absolute verbatim, relative anchored at the
		// link's directory (CheckPath resolves symlinks on both
		// sides and is a no-op without AllowedRoots).
		tp := so
		if !filepath.IsAbs(tp) {
			tp = filepath.Join(filepath.Dir(newp), tp)
		}
		if err := env.CheckPath(tp); err != nil {
			return nil, err
		}
		lerr := os.Symlink(so, newp)
		if le, ok := lerr.(*os.LinkError); ok {
			le.Old = stdlib.ScriptPathOf(so, so, le.Old)
			le.New = stdlib.ScriptPathOf(sn, newp, le.New)
		}
		return stdlib.ErrVal(lerr), nil
	}, os.Symlink)
	ospkg["Readlink"] = h.Fn1("os.Readlink", func(a []any) (any, error) {
		sp := stdlib.Str(a[0])
		p, err := env.FsPath(sp)
		if err != nil {
			return nil, err
		}
		t, rerr := os.Readlink(p)
		return stdlib.RetErr2(t, stdlib.ScriptPathErr(rerr, sp, p))
	}, os.Readlink)

}
