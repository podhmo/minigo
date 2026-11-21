package stdos

import (
	"fmt"
	"go/ast"
	"go/constant"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"syscall"

	intbind "github.com/podhmo/minigo/internal/bind"
	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/vm"
)

func Install(bind stdlib.Bind, h stdlib.Helpers, env intbind.Env) {
	// os: an interpreted program must never observe or terminate the host
	// process — Exit unwinds out as a process exit; the environment/argv surface is only
	// bound when the engine is unrestricted (no AllowedRoots). File-system
	// operations are always bound: each path argument resolves through
	// env.FsPath, which anchors relative paths at the engine's virtual cwd
	// and enforces AllowedRoots per call — that is the restricted-mode
	// file policy (host-surface gating stays per-symbol via WithHostPolicy).
	ospkg := map[string]runtime.Value{
		"Exit": h.Fn("os.Exit", func(a []any) (any, error) {
			// os.Exit unwinds past every defer straight out of the
			// interpreter — ExitRequest ends the run at the Call
			// boundary: 0 cleanly, N as `exit status N`.
			panic(&vm.ExitRequest{Code: int(stdlib.IntOf(a[0]))})
		}),
		"Stat":     h.Fn1("os.Stat", func(a []any) (any, error) { return fsOp2(env, "os.Stat", a, os.Stat) }),
		"Lstat":    h.Fn1("os.Lstat", func(a []any) (any, error) { return fsOp2(env, "os.Lstat", a, os.Lstat) }),
		"ReadFile": h.Fn1("os.ReadFile", func(a []any) (any, error) { return fsOp2(env, "os.ReadFile", a, os.ReadFile) }),
		"WriteFile": h.Fn3("os.WriteFile", func(a []any) (any, error) {
			sp := stdlib.Str(a[0])
			p, err := env.FsPath(sp)
			if err != nil {
				return nil, err
			}
			return stdlib.ErrVal(stdlib.ScriptPathErr(os.WriteFile(p, stdlib.ByteSlice(a[1]), fs.FileMode(stdlib.IntOf(a[2]))), sp, p)), nil
		}),
		"Mkdir": h.Fn2("os.Mkdir", func(a []any) (any, error) {
			sp := stdlib.Str(a[0])
			p, err := env.FsPath(sp)
			if err != nil {
				return nil, err
			}
			return stdlib.ErrVal(stdlib.ScriptPathErr(os.Mkdir(p, fs.FileMode(stdlib.IntOf(a[1]))), sp, p)), nil
		}),
		"MkdirAll": h.Fn2("os.MkdirAll", func(a []any) (any, error) {
			sp := stdlib.Str(a[0])
			p, err := env.FsPath(sp)
			if err != nil {
				return nil, err
			}
			return stdlib.ErrVal(stdlib.ScriptPathErr(os.MkdirAll(p, fs.FileMode(stdlib.IntOf(a[1]))), sp, p)), nil
		}),
		"Remove":    h.Fn1("os.Remove", func(a []any) (any, error) { return fsErrOp(env, "os.Remove", a, os.Remove) }),
		"RemoveAll": h.Fn1("os.RemoveAll", func(a []any) (any, error) { return fsErrOp(env, "os.RemoveAll", a, os.RemoveAll) }),
		"Chmod": h.Fn2("os.Chmod", func(a []any) (any, error) {
			sp := stdlib.Str(a[0])
			p, err := env.FsPath(sp)
			if err != nil {
				return nil, err
			}
			return stdlib.ErrVal(stdlib.ScriptPathErr(os.Chmod(p, fs.FileMode(stdlib.IntOf(a[1]))), sp, p)), nil
		}),
		"Truncate": h.Fn2("os.Truncate", func(a []any) (any, error) {
			sp := stdlib.Str(a[0])
			p, err := env.FsPath(sp)
			if err != nil {
				return nil, err
			}
			return stdlib.ErrVal(stdlib.ScriptPathErr(os.Truncate(p, stdlib.Int64Of(a[1])), sp, p)), nil
		}),
		"Rename": h.Fn2("os.Rename", func(a []any) (any, error) {
			old, err := env.FsPath(stdlib.Str(a[0]))
			if err != nil {
				return nil, err
			}
			newp, err := env.FsPath(stdlib.Str(a[1]))
			if err != nil {
				return nil, err
			}
			err = os.Rename(old, newp)
			if le, ok := err.(*os.LinkError); ok {
				// each side maps through its own resolution.
				le.Old = stdlib.ScriptPathOf(stdlib.Str(a[0]), old, le.Old)
				le.New = stdlib.ScriptPathOf(stdlib.Str(a[1]), newp, le.New)
			}
			return stdlib.ErrVal(err), nil
		}),
		"ReadDir": h.Fn1("os.ReadDir", func(a []any) (any, error) {
			sp := stdlib.Str(a[0])
			p, err := env.FsPath(sp)
			if err != nil {
				return nil, err
			}
			entries, err := os.ReadDir(p)
			err = stdlib.ScriptPathErr(err, sp, p)
			el := make([]runtime.Value, len(entries))
			for i, en := range entries {
				el[i] = &runtime.GoValue{V: en}
			}
			return &runtime.Tuple{Elems: []runtime.Value{
				&runtime.Slice{Elems: el, Typ: hostbridge.HostSliceTypOf(reflect.TypeFor[fs.DirEntry]())},
				stdlib.ErrVal(err)}}, nil
		}),
		// Getwd/Chdir operate on the engine's virtual cwd (see WithWorkingDir):
		// the host process cwd is never touched, so scripts can "cd" freely
		// without side effects on the embedding tool.
		"Getwd": h.Fn("os.Getwd", func(a []any) (any, error) { return stdlib.RetErr2(env.Cwd(), nil) }),
		"Chdir": h.Fn1("os.Chdir", func(a []any) (any, error) {
			sp := stdlib.Str(a[0])
			p, err := env.FsPath(sp)
			if err != nil {
				return nil, err
			}
			// gc reports chdir(2)'s failure: op "chdir" on the path as
			// the script spelled it.
			st, err := os.Stat(p)
			if err != nil {
				if pe, ok := err.(*fs.PathError); ok {
					return stdlib.ErrVal(&fs.PathError{Op: "chdir", Path: sp, Err: pe.Err}), nil
				}
				return stdlib.ErrVal(err), nil
			}
			if !st.IsDir() {
				return stdlib.ErrVal(&fs.PathError{Op: "chdir", Path: sp, Err: syscall.ENOTDIR}), nil
			}
			// the kernel's cwd is the RESOLVED directory — Getwd reads
			// it back without the spelled symlink or trailing slash.
			if rp, err := filepath.EvalSymlinks(p); err == nil {
				p = rp
			}
			env.SetCwd(p)
			return runtime.NIL, nil
		}),
		"Open":   h.Fn1("os.Open", func(a []any) (any, error) { return fsFileOp(env, a, os.Open) }),
		"Create": h.Fn1("os.Create", func(a []any) (any, error) { return fsFileOp(env, a, os.Create) }),
		"OpenFile": h.Fn3("os.OpenFile", func(a []any) (any, error) {
			sp := stdlib.Str(a[0])
			p, err := env.FsPath(sp)
			if err != nil {
				return nil, err
			}
			f, err := os.OpenFile(p, stdlib.IntOf(a[1]), fs.FileMode(stdlib.IntOf(a[2])))
			if err != nil {
				return stdlib.RetErr2(f, stdlib.ScriptPathErr(err, sp, p))
			}
			return stdlib.RetErr2(&scriptFile{File: f, name: sp}, nil)
		}),
		"MkdirTemp": h.Fn2("os.MkdirTemp", func(a []any) (any, error) {
			sp := stdlib.Str(a[0])
			dir, err := fsTempDir(env, sp)
			if err != nil {
				return nil, err
			}
			d, err := os.MkdirTemp(dir, stdlib.Str(a[1]))
			if err != nil {
				return stdlib.RetErr2(d, err)
			}
			// gc spells the result relative to the dir argument, not the
			// resolved path — MkdirTemp("sub", "x") is "sub/x123" — while
			// an empty dir reports os.TempDir()'s absolute path as is.
			if sp != "" {
				d = stdlib.ScriptPathOf(sp, dir, d)
			}
			return stdlib.RetErr2(d, nil)
		}),
		"CreateTemp": h.Fn2("os.CreateTemp", func(a []any) (any, error) {
			sp := stdlib.Str(a[0])
			dir, err := fsTempDir(env, sp)
			if err != nil {
				return nil, err
			}
			f, err := os.CreateTemp(dir, stdlib.Str(a[1]))
			if err != nil {
				return stdlib.RetErr2(f, err)
			}
			name := f.Name()
			if sp != "" {
				name = stdlib.ScriptPathOf(sp, dir, name)
			}
			return stdlib.RetErr2(&scriptFile{File: f, name: name}, nil)
		}),
		// DirFS resolves the dir through the same cwd+roots check as the
		// file ops; the returned host fs.DirFS keeps its Open rooted
		// there (os.DirFS itself never escapes — reads go through the
		// rooted handle, like Open).
		"DirFS": h.Fn1("os.DirFS", func(a []any) (any, error) {
			p, err := env.FsPath(stdlib.Str(a[0]))
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: os.DirFS(p)}, nil
		}),
		"IsPathSeparator": h.Fn1("os.IsPathSeparator", func(a []any) (any, error) {
			return os.IsPathSeparator(uint8(stdlib.IntOf(a[0]))), nil
		}, os.IsPathSeparator),
		"IsNotExist":   stdlib.OsErrPred("os.IsNotExist", os.IsNotExist),
		"IsExist":      stdlib.OsErrPred("os.IsExist", os.IsExist),
		"IsPermission": stdlib.OsErrPred("os.IsPermission", os.IsPermission),
		"IsTimeout":    stdlib.OsErrPred("os.IsTimeout", os.IsTimeout),
		// the error structs os's helpers type-switch on. PathError
		// aliases io/fs's source-interpreted typedef, so a script-built
		// &os.PathError{} is the same *fs.PathError twin hostErrArg
		// rebuilds for the host helpers — errors.As sees one type like
		// gc's `type PathError = fs.PathError`. LinkError/SyscallError
		// box the host struct: field writes in &os.LinkError{Err: ...}
		// reach the host fields through reflection.
		"PathError": &runtime.TypeDef{
			Name: "io/fs.PathError",
			Kind: runtime.KindAlias,
			Anon: &ast.SelectorExpr{X: ast.NewIdent("io/fs"), Sel: ast.NewIdent("PathError")},
		},
		"LinkError": stdlib.HostType("os.LinkError", func() any { return &os.LinkError{} }),
		// FileMode aliases io/fs's typedef like gc's
		// `type FileMode = fs.FileMode` — a host-typed binding would
		// mismatch the source-interpreted fs values (fs.ModeDir).
		"FileMode": &runtime.TypeDef{
			Name: "io/fs.FileMode",
			Kind: runtime.KindAlias,
			Anon: &ast.SelectorExpr{X: ast.NewIdent("io/fs"), Sel: ast.NewIdent("FileMode")},
		},
		"SyscallError": stdlib.HostType("os.SyscallError", func() any { return &os.SyscallError{} }),
		"NewSyscallError": &runtime.BuiltinFunc{
			Name:   "os.NewSyscallError",
			Target: os.NewSyscallError,
			Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
				if len(args) != 2 {
					return nil, fmt.Errorf("os.NewSyscallError needs 2 args, got %d", len(args))
				}
				// the error arg stays a script value — h.fn2's
				// hostbridge.GoNative flattening would drop a script error's
				// type and methods before hostErrOf can box them.
				return stdlib.ErrVal(os.NewSyscallError(stdlib.Str(hostbridge.GoNative(args[0])), hostbridge.HostErrArg(vc, args[1]))), nil
			},
		},
		"Link": h.Fn2("os.Link", func(a []any) (any, error) {
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
		}),
		// error sentinels for errors.Is on the script side
		"ErrNotExist": &runtime.GoValue{V: fs.ErrNotExist},
		// *os.File asserts (x/tools' gocommand checks cmd.Stdout's type)
		"File":           stdlib.HostType("os.File", func() any { return &os.File{} }),
		"Interrupt":      &runtime.GoValue{V: os.Interrupt},
		"Kill":           &runtime.GoValue{V: os.Kill},
		"ErrProcessDone": &runtime.GoValue{V: os.ErrProcessDone},
		"ErrExist":       &runtime.GoValue{V: fs.ErrExist},
		"ErrPermission":  &runtime.GoValue{V: fs.ErrPermission},
		"ErrClosed":      &runtime.GoValue{V: fs.ErrClosed},
		"ErrInvalid":     &runtime.GoValue{V: fs.ErrInvalid},
		"ErrNoDeadline":  &runtime.GoValue{V: os.ErrNoDeadline},
		// os.IsTimeout's sentinel (a script-built *fs.PathError may wrap it)
		"ErrDeadlineExceeded": &runtime.GoValue{V: os.ErrDeadlineExceeded},
		// consts
		"PathSeparator":     &runtime.UConst{V: constant.MakeInt64(os.PathSeparator), Rune: true},
		"PathListSeparator": &runtime.UConst{V: constant.MakeInt64(os.PathListSeparator), Rune: true},
		"DevNull":           os.DevNull,
		"O_RDONLY":          int64(os.O_RDONLY),
		"O_WRONLY":          int64(os.O_WRONLY),
		"O_RDWR":            int64(os.O_RDWR),
		"O_APPEND":          int64(os.O_APPEND),
		"O_CREATE":          int64(os.O_CREATE),
		"O_EXCL":            int64(os.O_EXCL),
		"O_SYNC":            int64(os.O_SYNC),
		"O_TRUNC":           int64(os.O_TRUNC),
		"ModeDir":           &runtime.GoValue{V: fs.ModeDir},
		"ModeAppend":        &runtime.GoValue{V: fs.ModeAppend},
		"ModeExclusive":     &runtime.GoValue{V: fs.ModeExclusive},
		"ModeTemporary":     &runtime.GoValue{V: fs.ModeTemporary},
		"ModeSymlink":       &runtime.GoValue{V: fs.ModeSymlink},
		"ModeNamedPipe":     &runtime.GoValue{V: fs.ModeNamedPipe},
		"ModeSocket":        &runtime.GoValue{V: fs.ModeSocket},
		"ModeSetuid":        &runtime.GoValue{V: fs.ModeSetuid},
		"ModeSetgid":        &runtime.GoValue{V: fs.ModeSetgid},
		"ModeCharDevice":    &runtime.GoValue{V: fs.ModeCharDevice},
		"ModeSticky":        &runtime.GoValue{V: fs.ModeSticky},
		"ModeIrregular":     &runtime.GoValue{V: fs.ModeIrregular},
		"ModePerm":          &runtime.GoValue{V: fs.ModePerm},
		"ModeType":          &runtime.GoValue{V: fs.ModeType},
		"SeekStart":         &runtime.UConst{V: constant.MakeInt64(io.SeekStart)},
		"SeekCurrent":       &runtime.UConst{V: constant.MakeInt64(io.SeekCurrent)},
		"SeekEnd":           &runtime.UConst{V: constant.MakeInt64(io.SeekEnd)},
		// marker interface typedefs so `os.DirEntry`/`os.FileInfo` resolve
		// in callback signatures (filepath.WalkDir's funclit); member
		// access on the host values behind them dispatches by reflection.
		"DirEntry": &runtime.TypeDef{Name: "DirEntry", Kind: runtime.KindInterface},
		"FileInfo": &runtime.TypeDef{Name: "FileInfo", Kind: runtime.KindInterface},
	}

	if len(env.AllowedRoots()) == 0 {
		ospkg["Getenv"] = h.Fn("os.Getenv", func(a []any) (any, error) { return os.Getenv(stdlib.Str(a[0])), nil })
		ospkg["Setenv"] = h.Fn2("os.Setenv", func(a []any) (any, error) { return stdlib.ErrVal(os.Setenv(stdlib.Str(a[0]), stdlib.Str(a[1]))), nil })
		ospkg["Unsetenv"] = h.Fn1("os.Unsetenv", func(a []any) (any, error) { return stdlib.ErrVal(os.Unsetenv(stdlib.Str(a[0]))), nil })
		ospkg["Clearenv"] = h.Fn("os.Clearenv", func(a []any) (any, error) { os.Clearenv(); return nil, nil })
		ospkg["Environ"] = h.Fn("os.Environ", func(a []any) (any, error) { return stdlib.StrsSlice(os.Environ()), nil })
		// os.Args is the script-visible argv (WithArgs; else the host
		// process argv), bound as a VARIABLE like Go's — flag's package
		// init reads it via len(os.Args).
		argv := os.Args
		if args := env.Args(); args != nil {
			argv = args
		}
		ospkg["Args"] = stdlib.StrsSlice(argv)
		ospkg["Hostname"] = h.Fn("os.Hostname", func(a []any) (any, error) { return stdlib.RetErr2(os.Hostname()) })
		// process stdio, boxed for cmd.Stdout / cmd.Stderr wiring.
		// os.Stdout is a *os.File facade whose writes forward to the
		// engine's configured output so host consumers handed it —
		// text/template's Execute above all — write where print/println
		// write instead of escaping to the process stdout. (Stdin/Stderr
		// keep the real files: there is no engine-level abstraction for
		// them.)
		ospkg["Stdin"] = &runtime.GoValue{V: os.Stdin}
		ospkg["Stdout"] = &runtime.GoValue{V: &EngineStdout{File: os.Stdout, env: env}}
		ospkg["Stderr"] = &runtime.GoValue{V: os.Stderr}
		ospkg["Pipe"] = h.Fn("os.Pipe", func(a []any) (any, error) {
			r, w, err := os.Pipe()
			return &runtime.Tuple{Elems: []runtime.Value{&runtime.GoValue{V: r}, &runtime.GoValue{V: w}, stdlib.ErrVal(err)}}, nil
		})
		ospkg["TempDir"] = h.Fn("os.TempDir", func(a []any) (any, error) { return os.TempDir(), nil })
		ospkg["UserHomeDir"] = h.Fn("os.UserHomeDir", func(a []any) (any, error) { return stdlib.RetErr2(os.UserHomeDir()) })
		ospkg["UserCacheDir"] = h.Fn("os.UserCacheDir", func(a []any) (any, error) { return stdlib.RetErr2(os.UserCacheDir()) })
		ospkg["UserConfigDir"] = h.Fn("os.UserConfigDir", func(a []any) (any, error) { return stdlib.RetErr2(os.UserConfigDir()) })
	}

	bind("os", ospkg)
}

// fsOp2 applies a one-path os operation after resolving the script path
// through the virtual cwd + AllowedRoots check; the result goes back as a
// Go-style (value, err) tuple.
func fsOp2[T any](env intbind.Env, name string, a []any, op func(string) (T, error)) (any, error) {
	sp := stdlib.Str(a[0])
	p, err := env.FsPath(sp)
	if err != nil {
		return nil, err
	}
	v, err := op(p)
	return stdlib.RetErr2(v, stdlib.ScriptPathErr(err, sp, p))
}

func fsErrOp(env intbind.Env, name string, a []any, op func(string) error) (any, error) {
	sp := stdlib.Str(a[0])
	p, err := env.FsPath(sp)
	if err != nil {
		return nil, err
	}
	return stdlib.ErrVal(stdlib.ScriptPathErr(op(p), sp, p)), nil
}

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
