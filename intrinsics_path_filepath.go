package minigo

import (
	"fmt"
	"go/constant"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installPathFilepath(h *hostHelpers) {
	// path/filepath: pure path math is always available (generated);
	// operations that touch the filesystem (Glob, WalkDir, Walk,
	// EvalSymlinks) go through env.FsPath like the os.* equivalents.
	tbl := pathFilepathGenEntries()
	tbl["Separator"] = &runtime.UConst{V: constant.MakeInt64(os.PathSeparator), Rune: true}
	tbl["ListSeparator"] = &runtime.UConst{V: constant.MakeInt64(os.PathListSeparator), Rune: true}
	// Abs/Rel anchor relative paths at the engine's virtual cwd, not the
	// host process's (divergence from real filepath.Abs is deliberate).
	tbl["Abs"] = h.fn1("filepath.Abs", func(a []any) (any, error) { return retErr2(h.env.CwdAbs(str(a[0])), nil) })
	tbl["Rel"] = h.fn2("filepath.Rel", func(a []any) (any, error) {
		return retErr2(filepath.Rel(h.env.CwdAbs(str(a[0])), h.env.CwdAbs(str(a[1]))))
	})
	tbl["EvalSymlinks"] = h.fn1("filepath.EvalSymlinks", func(a []any) (any, error) {
		sp := str(a[0])
		p, err := h.env.FsPath(sp)
		if err != nil {
			return nil, err
		}
		r, err := filepath.EvalSymlinks(p)
		return retErr2(r, scriptPathErr(err, sp, p))
	})
	tbl["Glob"] = h.fn1("filepath.Glob", func(a []any) (any, error) {
		pat := str(a[0])
		ap, err := h.env.FsPath(pat)
		if err != nil {
			return nil, err
		}
		m, err := filepath.Glob(ap)
		if err != nil {
			return retErr2([]string(nil), err)
		}
		// A pattern inside the roots can still expand through an in-root
		// symlink into files outside them; matches are re-checked and an
		// escape comes back as the call's error value (Go's shape), not
		// a trap — the script gets nil matches either way.
		for _, p := range m {
			if err := h.env.CheckPath(p); err != nil {
				return retErr2([]string(nil), err)
			}
		}
		if !filepath.IsAbs(pat) {
			// Go returns matches in the shape of the pattern: keep
			// relative patterns relative to the virtual cwd.
			for i, p := range m {
				if rel, rerr := filepath.Rel(h.env.Cwd(), p); rerr == nil {
					m[i] = rel
				}
			}
		}
		return retErr2(m, nil)
	})
	tbl["WalkDir"] = &runtime.BuiltinFunc{Name: "filepath.WalkDir", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("filepath.WalkDir needs 2 args, got %d", len(args))
		}
		root := str(goNative(args[0]))
		cb := args[1]
		ap, err := h.env.FsPath(root)
		if err != nil {
			return nil, err
		}
		werr := filepath.WalkDir(ap, func(p string, d fs.DirEntry, werr error) error {
			var dv runtime.Value = runtime.NIL
			if d != nil {
				if p == ap {
					d = walkRootEntry(d, hostSpelling(h.env, root))
				}
				dv = &runtime.GoValue{V: d}
			}
			return walkCallback(v, cb, root, ap, p, dv, werr)
		})
		return errVal(werr), nil
	}}
	tbl["Walk"] = &runtime.BuiltinFunc{Name: "filepath.Walk", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("filepath.Walk needs 2 args, got %d", len(args))
		}
		root := str(goNative(args[0]))
		cb := args[1]
		ap, err := h.env.FsPath(root)
		if err != nil {
			return nil, err
		}
		werr := filepath.Walk(ap, func(p string, info fs.FileInfo, werr error) error {
			var iv runtime.Value = runtime.NIL
			if info != nil {
				if p == ap {
					info = walkRootInfo(info, hostSpelling(h.env, root))
				}
				iv = &runtime.GoValue{V: info}
			}
			return walkCallback(v, cb, root, ap, p, iv, werr)
		})
		return errVal(werr), nil
	}}
	e.Bind("path/filepath", tbl)
}
