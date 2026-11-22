package filepath

import (
	"fmt"
	"go/constant"
	"io/fs"
	"os"
	"path/filepath"

	intbind "github.com/podhmo/minigo/internal/bind"
	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
)

func Install(bind stdlib.Bind, h stdlib.Helpers, env intbind.Env) {
	// path/filepath: pure path math is always available (generated);
	// operations that touch the filesystem (Glob, WalkDir, Walk,
	// EvalSymlinks) go through env.FsPath like the os.* equivalents.
	tbl := pathFilepathGenEntries()
	tbl["Separator"] = &runtime.UConst{V: constant.MakeInt64(os.PathSeparator), Rune: true}
	tbl["ListSeparator"] = &runtime.UConst{V: constant.MakeInt64(os.PathListSeparator), Rune: true}
	// Abs/Rel anchor relative paths at the engine's virtual cwd, not the
	// host process's (divergence from real filepath.Abs is deliberate).
	tbl["Abs"] = h.Fn1("filepath.Abs", func(a []any) (any, error) { return stdlib.RetErr2(env.CwdAbs(stdlib.Str(a[0])), nil) }, filepath.Abs)
	tbl["Rel"] = h.Fn2("filepath.Rel", func(a []any) (any, error) {
		return stdlib.RetErr2(filepath.Rel(env.CwdAbs(stdlib.Str(a[0])), env.CwdAbs(stdlib.Str(a[1]))))
	}, filepath.Rel)
	tbl["EvalSymlinks"] = h.Fn1("filepath.EvalSymlinks", func(a []any) (any, error) {
		sp := stdlib.Str(a[0])
		p, err := env.FsPath(sp)
		if err != nil {
			return nil, err
		}
		r, err := filepath.EvalSymlinks(p)
		return stdlib.RetErr2(r, stdlib.ScriptPathErr(err, sp, p))
	}, filepath.EvalSymlinks)
	tbl["Glob"] = h.Fn1("filepath.Glob", func(a []any) (any, error) {
		pat := stdlib.Str(a[0])
		ap, err := env.FsPath(pat)
		if err != nil {
			return nil, err
		}
		m, err := filepath.Glob(ap)
		if err != nil {
			return stdlib.RetErr2([]string(nil), err)
		}
		// A pattern inside the roots can still expand through an in-root
		// symlink into files outside them; matches are re-checked and an
		// escape comes back as the call's error value (Go's shape), not
		// a trap — the script gets nil matches either way.
		for _, p := range m {
			if err := env.CheckPath(p); err != nil {
				return stdlib.RetErr2([]string(nil), err)
			}
		}
		if !filepath.IsAbs(pat) {
			// Go returns matches in the shape of the pattern: keep
			// relative patterns relative to the virtual cwd.
			for i, p := range m {
				if rel, rerr := filepath.Rel(env.Cwd(), p); rerr == nil {
					m[i] = rel
				}
			}
		}
		return stdlib.RetErr2(m, nil)
	}, filepath.Glob)
	tbl["WalkDir"] = &runtime.BuiltinFunc{Name: "filepath.WalkDir", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("filepath.WalkDir needs 2 args, got %d", len(args))
		}
		root := stdlib.Str(hostbridge.GoNative(args[0]))
		cb := args[1]
		ap, err := env.FsPath(root)
		if err != nil {
			return nil, err
		}
		werr := filepath.WalkDir(ap, func(p string, d fs.DirEntry, werr error) error {
			var dv runtime.Value = runtime.NIL
			if d != nil {
				if p == ap {
					d = walkRootEntry(d, hostSpelling(env, root))
				}
				dv = &runtime.GoValue{V: d}
			}
			return walkCallback(v, cb, root, ap, p, dv, werr)
		})
		return stdlib.ErrVal(werr), nil
	}}
	tbl["Walk"] = &runtime.BuiltinFunc{Name: "filepath.Walk", SyncCallbacks: true, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("filepath.Walk needs 2 args, got %d", len(args))
		}
		root := stdlib.Str(hostbridge.GoNative(args[0]))
		cb := args[1]
		ap, err := env.FsPath(root)
		if err != nil {
			return nil, err
		}
		werr := filepath.Walk(ap, func(p string, info fs.FileInfo, werr error) error {
			var iv runtime.Value = runtime.NIL
			if info != nil {
				if p == ap {
					info = walkRootInfo(info, hostSpelling(env, root))
				}
				iv = &runtime.GoValue{V: info}
			}
			return walkCallback(v, cb, root, ap, p, iv, werr)
		})
		return stdlib.ErrVal(werr), nil
	}}
	bind("path/filepath", tbl)
}

// hostSpelling anchors a script path at the virtual cwd WITHOUT
// cleaning it, so a host lstat names the result like gc's lstat of the
// spelled path (basename "." for ".", ".." for "a/b/..").
func hostSpelling(env intbind.Env, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return env.Cwd() + string(filepath.Separator) + p
}

// walkRootEntry/walkRootInfo give the walk root gc's entry: gc lstats
// the root as written, so its Name() is the base of the script's
// spelling — the host walked the resolved absolute path, whose base is
// the directory's own name. Re-lstatting the uncleaned host spelling
// yields the same host types gc hands over (fs.FileInfoToDirEntry's
// entry, *os.fileStat); on failure the host's own entry stands.
func walkRootEntry(d fs.DirEntry, hp string) fs.DirEntry {
	if filepath.Base(hp) == d.Name() {
		return d
	}
	if fi, err := os.Lstat(hp); err == nil {
		return fs.FileInfoToDirEntry(fi)
	}
	return d
}

func walkRootInfo(fi fs.FileInfo, hp string) fs.FileInfo {
	if filepath.Base(hp) == fi.Name() {
		return fi
	}
	if li, err := os.Lstat(hp); err == nil {
		return li
	}
	return fi
}

// callOrPanic invokes a script callback from inside a host call and
// re-raises any failure as a Go panic: v.Call delivers a script panic
// (or trap) as an error across its boundary, but returning that as the
// builtin's error would turn a recoverable panic into a runtime trap —
// in Go a failure inside strings.Map's callback just propagates as a
// panic, and a trap or process exit keeps its own kind through
// asScriptPanic.
// walkCallback runs a filepath.Walk/WalkDir script callback for host
// path p: the walk ran from ap, the engine's resolution of the script's
// root, so p maps back onto root as written (gc hands the root over
// verbatim — "./a", "a/" — and joins descendants onto it), entry is the
// boxed fs.DirEntry/fs.FileInfo (NIL when the host passed none), and
// the result goes back through hostErrOf so the host walker compares
// fs.SkipDir/SkipAll by identity and a script error keeps its identity
// on the way out (errVal).
func walkCallback(v runtime.MemberCaller, cb runtime.Value, root, ap, p string, entry runtime.Value, werr error) error {
	sp := stdlib.ScriptPathOf(root, ap, p)
	r := hostbridge.CallOrPanic(v, cb, []runtime.Value{sp, entry, stdlib.ErrVal(stdlib.ScriptPathErr(werr, root, ap))})
	return hostbridge.HostErrOf(v, r)
}
