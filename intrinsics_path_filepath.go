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
	// path/filepath: pure path math is always available; operations that
	// touch the filesystem (Glob, WalkDir, EvalSymlinks) go through
	// env.FsPath like the os.* equivalents.
	e.Bind("path/filepath", map[string]runtime.Value{
		"Join":          h.fn("filepath.Join", func(a []any) (any, error) { return filepath.Join(strSlice(a)...), nil }, filepath.Join),
		"Base":          h.fn1("filepath.Base", func(a []any) (any, error) { return filepath.Base(str(a[0])), nil }, filepath.Base),
		"Dir":           h.fn1("filepath.Dir", func(a []any) (any, error) { return filepath.Dir(str(a[0])), nil }, filepath.Dir),
		"Ext":           h.fn1("filepath.Ext", func(a []any) (any, error) { return filepath.Ext(str(a[0])), nil }, filepath.Ext),
		"Clean":         h.fn1("filepath.Clean", func(a []any) (any, error) { return filepath.Clean(str(a[0])), nil }, filepath.Clean),
		"VolumeName":    h.fn1("filepath.VolumeName", func(a []any) (any, error) { return filepath.VolumeName(str(a[0])), nil }, filepath.VolumeName),
		"IsAbs":         h.fn1("filepath.IsAbs", func(a []any) (any, error) { return filepath.IsAbs(str(a[0])), nil }, filepath.IsAbs),
		"ToSlash":       h.fn1("filepath.ToSlash", func(a []any) (any, error) { return filepath.ToSlash(str(a[0])), nil }, filepath.ToSlash),
		"FromSlash":     h.fn1("filepath.FromSlash", func(a []any) (any, error) { return filepath.FromSlash(str(a[0])), nil }, filepath.FromSlash),
		"SplitList":     h.fn1("filepath.SplitList", func(a []any) (any, error) { return filepath.SplitList(str(a[0])), nil }, filepath.SplitList),
		"Match":         h.fn2("filepath.Match", func(a []any) (any, error) { return retErr2(filepath.Match(str(a[0]), str(a[1]))) }),
		"Separator":     &runtime.UConst{V: constant.MakeInt64(os.PathSeparator), Rune: true},
		"ListSeparator": &runtime.UConst{V: constant.MakeInt64(os.PathListSeparator), Rune: true},
		"Split": h.fn1("filepath.Split", func(a []any) (any, error) {
			d, f := filepath.Split(str(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{d, f}}, nil
		}),
		// Abs/Rel anchor relative paths at the engine's virtual cwd, not the
		// host process's (divergence from real filepath.Abs is deliberate).
		"Abs": h.fn1("filepath.Abs", func(a []any) (any, error) { return retErr2(h.env.CwdAbs(str(a[0])), nil) }),
		"Rel": h.fn2("filepath.Rel", func(a []any) (any, error) {
			return retErr2(filepath.Rel(h.env.CwdAbs(str(a[0])), h.env.CwdAbs(str(a[1]))))
		}),
		"EvalSymlinks": h.fn1("filepath.EvalSymlinks", func(a []any) (any, error) {
			sp := str(a[0])
			p, err := h.env.FsPath(sp)
			if err != nil {
				return nil, err
			}
			r, err := filepath.EvalSymlinks(p)
			return retErr2(r, scriptPathErr(err, sp, p))
		}),
		"Glob": h.fn1("filepath.Glob", func(a []any) (any, error) {
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
		}),
		"WalkDir": &runtime.BuiltinFunc{Name: "filepath.WalkDir", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
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
		}},
		"Walk": &runtime.BuiltinFunc{Name: "filepath.Walk", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
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
		}},
		"SkipDir": &runtime.GoValue{V: filepath.SkipDir},
		"SkipAll": &runtime.GoValue{V: filepath.SkipAll},
		// filepath.ErrBadPattern is its own sentinel, distinct from
		// path.ErrBadPattern (filepath.Match returns this one).
		"ErrBadPattern": errVal(filepath.ErrBadPattern),
	})
}
