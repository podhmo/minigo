package minigo

import (
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/podhmo/minigo/internal/bind"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/vm"
)

func (e *Engine) installOs(h *hostHelpers) {
	// os: an interpreted program must never observe or terminate the host
	// process — Exit unwinds out as a process exit; the environment/argv surface is only
	// bound when the engine is unrestricted (no AllowedRoots). File-system
	// operations are always bound: each path argument resolves through
	// env.FsPath, which anchors relative paths at the engine's virtual cwd
	// and enforces AllowedRoots per call — that is the restricted-mode
	// file policy (host-surface gating stays per-symbol via WithHostPolicy).
	ospkg := map[string]runtime.Value{
		"Exit": h.fn("os.Exit", func(a []any) (any, error) {
			// os.Exit unwinds past every defer straight out of the
			// interpreter — ExitRequest ends the run at the Call
			// boundary: 0 cleanly, N as `exit status N`.
			panic(&vm.ExitRequest{Code: int(intOf(a[0]))})
		}),
		"Stat":     h.fn1("os.Stat", func(a []any) (any, error) { return fsOp2(h.env, "os.Stat", a, os.Stat) }),
		"Lstat":    h.fn1("os.Lstat", func(a []any) (any, error) { return fsOp2(h.env, "os.Lstat", a, os.Lstat) }),
		"ReadFile": h.fn1("os.ReadFile", func(a []any) (any, error) { return fsOp2(h.env, "os.ReadFile", a, os.ReadFile) }),
		"WriteFile": h.fn3("os.WriteFile", func(a []any) (any, error) {
			sp := str(a[0])
			p, err := h.env.FsPath(sp)
			if err != nil {
				return nil, err
			}
			return errVal(scriptPathErr(os.WriteFile(p, byteSlice(a[1]), fs.FileMode(intOf(a[2]))), sp, p)), nil
		}),
		"Mkdir": h.fn2("os.Mkdir", func(a []any) (any, error) {
			sp := str(a[0])
			p, err := h.env.FsPath(sp)
			if err != nil {
				return nil, err
			}
			return errVal(scriptPathErr(os.Mkdir(p, fs.FileMode(intOf(a[1]))), sp, p)), nil
		}),
		"MkdirAll": h.fn2("os.MkdirAll", func(a []any) (any, error) {
			sp := str(a[0])
			p, err := h.env.FsPath(sp)
			if err != nil {
				return nil, err
			}
			return errVal(scriptPathErr(os.MkdirAll(p, fs.FileMode(intOf(a[1]))), sp, p)), nil
		}),
		"Remove":    h.fn1("os.Remove", func(a []any) (any, error) { return fsErrOp(h.env, "os.Remove", a, os.Remove) }),
		"RemoveAll": h.fn1("os.RemoveAll", func(a []any) (any, error) { return fsErrOp(h.env, "os.RemoveAll", a, os.RemoveAll) }),
		"Chmod": h.fn2("os.Chmod", func(a []any) (any, error) {
			sp := str(a[0])
			p, err := h.env.FsPath(sp)
			if err != nil {
				return nil, err
			}
			return errVal(scriptPathErr(os.Chmod(p, fs.FileMode(intOf(a[1]))), sp, p)), nil
		}),
		"Truncate": h.fn2("os.Truncate", func(a []any) (any, error) {
			sp := str(a[0])
			p, err := h.env.FsPath(sp)
			if err != nil {
				return nil, err
			}
			return errVal(scriptPathErr(os.Truncate(p, int64Of(a[1])), sp, p)), nil
		}),
		"Rename": h.fn2("os.Rename", func(a []any) (any, error) {
			old, err := h.env.FsPath(str(a[0]))
			if err != nil {
				return nil, err
			}
			newp, err := h.env.FsPath(str(a[1]))
			if err != nil {
				return nil, err
			}
			err = os.Rename(old, newp)
			if le, ok := err.(*os.LinkError); ok {
				// each side maps through its own resolution.
				le.Old = scriptPathOf(str(a[0]), old, le.Old)
				le.New = scriptPathOf(str(a[1]), newp, le.New)
			}
			return errVal(err), nil
		}),
		"ReadDir": h.fn1("os.ReadDir", func(a []any) (any, error) {
			sp := str(a[0])
			p, err := h.env.FsPath(sp)
			if err != nil {
				return nil, err
			}
			entries, err := os.ReadDir(p)
			err = scriptPathErr(err, sp, p)
			el := make([]runtime.Value, len(entries))
			for i, en := range entries {
				el[i] = &runtime.GoValue{V: en}
			}
			return &runtime.Tuple{Elems: []runtime.Value{&runtime.Slice{Elems: el}, errVal(err)}}, nil
		}),
		// Getwd/Chdir operate on the engine's virtual cwd (see WithWorkingDir):
		// the host process cwd is never touched, so scripts can "cd" freely
		// without side effects on the embedding tool.
		"Getwd": h.fn("os.Getwd", func(a []any) (any, error) { return retErr2(h.env.Cwd(), nil) }),
		"Chdir": h.fn1("os.Chdir", func(a []any) (any, error) {
			sp := str(a[0])
			p, err := h.env.FsPath(sp)
			if err != nil {
				return nil, err
			}
			// gc reports chdir(2)'s failure: op "chdir" on the path as
			// the script spelled it.
			st, err := os.Stat(p)
			if err != nil {
				if pe, ok := err.(*fs.PathError); ok {
					return errVal(&fs.PathError{Op: "chdir", Path: sp, Err: pe.Err}), nil
				}
				return errVal(err), nil
			}
			if !st.IsDir() {
				return errVal(&fs.PathError{Op: "chdir", Path: sp, Err: syscall.ENOTDIR}), nil
			}
			h.env.SetCwd(p)
			return runtime.NIL, nil
		}),
		"Open":   h.fn1("os.Open", func(a []any) (any, error) { return fsOp2(h.env, "os.Open", a, os.Open) }),
		"Create": h.fn1("os.Create", func(a []any) (any, error) { return fsOp2(h.env, "os.Create", a, os.Create) }),
		"OpenFile": h.fn3("os.OpenFile", func(a []any) (any, error) {
			sp := str(a[0])
			p, err := h.env.FsPath(sp)
			if err != nil {
				return nil, err
			}
			f, err := os.OpenFile(p, intOf(a[1]), fs.FileMode(intOf(a[2])))
			return retErr2(f, scriptPathErr(err, sp, p))
		}),
		"MkdirTemp": h.fn2("os.MkdirTemp", func(a []any) (any, error) {
			dir, err := fsTempDir(h.env, str(a[0]))
			if err != nil {
				return nil, err
			}
			return retErr2(os.MkdirTemp(dir, str(a[1])))
		}),
		"CreateTemp": h.fn2("os.CreateTemp", func(a []any) (any, error) {
			dir, err := fsTempDir(h.env, str(a[0]))
			if err != nil {
				return nil, err
			}
			return retErr2(os.CreateTemp(dir, str(a[1])))
		}),
		// DirFS resolves the dir through the same cwd+roots check as the
		// file ops; the returned host fs.DirFS keeps its Open rooted
		// there (os.DirFS itself never escapes — reads go through the
		// rooted handle, like Open).
		"DirFS": h.fn1("os.DirFS", func(a []any) (any, error) {
			p, err := h.env.FsPath(str(a[0]))
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: os.DirFS(p)}, nil
		}),
		"IsNotExist":   osErrPred("os.IsNotExist", os.IsNotExist),
		"IsExist":      osErrPred("os.IsExist", os.IsExist),
		"IsPermission": osErrPred("os.IsPermission", os.IsPermission),
		"IsTimeout":    osErrPred("os.IsTimeout", os.IsTimeout),
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
		"LinkError":    hostType("os.LinkError", func() any { return &os.LinkError{} }),
		"SyscallError": hostType("os.SyscallError", func() any { return &os.SyscallError{} }),
		"NewSyscallError": &runtime.BuiltinFunc{
			Name:   "os.NewSyscallError",
			Target: os.NewSyscallError,
			Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
				if len(args) != 2 {
					return nil, fmt.Errorf("os.NewSyscallError needs 2 args, got %d", len(args))
				}
				// the error arg stays a script value — h.fn2's
				// goNative flattening would drop a script error's
				// type and methods before hostErrOf can box them.
				return errVal(os.NewSyscallError(str(goNative(args[0])), hostErrArg(vc, args[1]))), nil
			},
		},
		"Link": h.fn2("os.Link", func(a []any) (any, error) {
			so, sn := str(a[0]), str(a[1])
			old, err := h.env.FsPath(so)
			if err != nil {
				return nil, err
			}
			newp, err := h.env.FsPath(sn)
			if err != nil {
				return nil, err
			}
			lerr := os.Link(old, newp)
			if le, ok := lerr.(*os.LinkError); ok {
				// the host error spells the resolved paths — rewrite
				// both halves to the script's spelling like
				// scriptPathErr does for *fs.PathError.
				le.Old = scriptPathOf(so, old, le.Old)
				le.New = scriptPathOf(sn, newp, le.New)
			}
			return errVal(lerr), nil
		}),
		// error sentinels for errors.Is on the script side
		"ErrNotExist": &runtime.GoValue{V: fs.ErrNotExist},
		// *os.File asserts (x/tools' gocommand checks cmd.Stdout's type)
		"File":           hostType("os.File", func() any { return &os.File{} }),
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

	if len(h.env.AllowedRoots()) == 0 {
		ospkg["Getenv"] = h.fn("os.Getenv", func(a []any) (any, error) { return os.Getenv(str(a[0])), nil })
		ospkg["Setenv"] = h.fn2("os.Setenv", func(a []any) (any, error) { return errVal(os.Setenv(str(a[0]), str(a[1]))), nil })
		ospkg["Unsetenv"] = h.fn1("os.Unsetenv", func(a []any) (any, error) { return errVal(os.Unsetenv(str(a[0]))), nil })
		ospkg["Clearenv"] = h.fn("os.Clearenv", func(a []any) (any, error) { os.Clearenv(); return nil, nil })
		ospkg["Environ"] = h.fn("os.Environ", func(a []any) (any, error) { return strsSlice(os.Environ()), nil })
		// os.Args is the script-visible argv (WithArgs; else the host
		// process argv), bound as a VARIABLE like Go's — flag's package
		// init reads it via len(os.Args).
		argv := os.Args
		if args := h.env.Args(); args != nil {
			argv = args
		}
		ospkg["Args"] = strsSlice(argv)
		ospkg["Hostname"] = h.fn("os.Hostname", func(a []any) (any, error) { return retErr2(os.Hostname()) })
		// process stdio, boxed for cmd.Stdout / cmd.Stderr wiring.
		// os.Stdout is a *os.File facade whose writes forward to the
		// engine's configured output so host consumers handed it —
		// text/template's Execute above all — write where print/println
		// write instead of escaping to the process stdout. (Stdin/Stderr
		// keep the real files: there is no engine-level abstraction for
		// them.)
		ospkg["Stdin"] = &runtime.GoValue{V: os.Stdin}
		ospkg["Stdout"] = &runtime.GoValue{V: &engineStdout{File: os.Stdout, h: h}}
		ospkg["Stderr"] = &runtime.GoValue{V: os.Stderr}
		ospkg["Pipe"] = h.fn("os.Pipe", func(a []any) (any, error) {
			r, w, err := os.Pipe()
			return &runtime.Tuple{Elems: []runtime.Value{&runtime.GoValue{V: r}, &runtime.GoValue{V: w}, errVal(err)}}, nil
		})
		ospkg["TempDir"] = h.fn("os.TempDir", func(a []any) (any, error) { return os.TempDir(), nil })
		ospkg["UserHomeDir"] = h.fn("os.UserHomeDir", func(a []any) (any, error) { return retErr2(os.UserHomeDir()) })
		ospkg["UserCacheDir"] = h.fn("os.UserCacheDir", func(a []any) (any, error) { return retErr2(os.UserCacheDir()) })
		ospkg["UserConfigDir"] = h.fn("os.UserConfigDir", func(a []any) (any, error) { return retErr2(os.UserConfigDir()) })
	}

	e.Bind("os", ospkg)
}

// fsOp2 applies a one-path os operation after resolving the script path
// through the virtual cwd + AllowedRoots check; the result goes back as a
// Go-style (value, err) tuple.
func fsOp2[T any](env bind.Env, name string, a []any, op func(string) (T, error)) (any, error) {
	sp := str(a[0])
	p, err := env.FsPath(sp)
	if err != nil {
		return nil, err
	}
	v, err := op(p)
	return retErr2(v, scriptPathErr(err, sp, p))
}

func fsErrOp(env bind.Env, name string, a []any, op func(string) error) (any, error) {
	sp := str(a[0])
	p, err := env.FsPath(sp)
	if err != nil {
		return nil, err
	}
	return errVal(scriptPathErr(op(p), sp, p)), nil
}

// scriptPathOf maps a host path back to the script's spelling: ap is
// the engine's resolved form (fsPath) of the script path sp, so ap
// itself reads back as sp verbatim and a descendant of ap joins onto sp
// (filepath.Join cleans, like the host's own joins from sp would).
// Other paths pass through.
//
// A proper ancestor of ap (MkdirAll failing on an intermediate
// component) is sp with as many trailing elements dropped — gc slices
// the spelled path — provided that prefix of sp still resolves to p.
func scriptPathOf(sp, ap, p string) string {
	if p == ap {
		return sp
	}
	if rest, ok := cutPathPrefix(p, ap); ok {
		return filepath.Join(sp, rest)
	}
	if rest, ok := cutPathPrefix(ap, p); ok {
		t := sp
		for range strings.Split(rest, string(filepath.Separator)) {
			t = strings.TrimRight(t, string(filepath.Separator))
			i := strings.LastIndexByte(t, filepath.Separator)
			if i < 0 {
				return p
			}
			t = t[:i]
			if t == "" {
				t = string(filepath.Separator)
			}
		}
		if t = strings.TrimRight(t, string(filepath.Separator)); t == "" {
			t = string(filepath.Separator)
		}
		if resolveLike(sp, ap, t) == p {
			return t
		}
	}
	return p
}

// cutPathPrefix reports p's remainder below the directory dir.
func cutPathPrefix(p, dir string) (string, bool) {
	rest, ok := strings.CutPrefix(p, dir)
	if !ok || rest == "" {
		return "", false
	}
	if strings.HasSuffix(dir, string(filepath.Separator)) {
		return rest, true
	}
	return strings.CutPrefix(rest, string(filepath.Separator))
}

// resolveLike resolves the spelled path t the way fsPath resolved sp to
// ap — against the same base directory — or "" when the base cannot be
// recovered from the pair.
func resolveLike(sp, ap, t string) string {
	if filepath.IsAbs(t) {
		return filepath.Clean(t)
	}
	csp := filepath.Clean(sp)
	if csp == "." {
		return filepath.Join(ap, t)
	}
	base, ok := strings.CutSuffix(ap, string(filepath.Separator)+csp)
	if !ok || strings.HasPrefix(csp, "..") {
		return ""
	}
	return filepath.Join(base, t)
}

// scriptPathErr rewrites the path an os/filepath error reports from the
// host form the engine resolved (absolute, under the virtual cwd) back
// to the script's spelling — gc says `open nope: ...` for
// os.ReadFile("nope"). The host error is fresh, so it is rewritten in
// place and keeps its identity.
func scriptPathErr(err error, sp, ap string) error {
	switch x := err.(type) {
	case *fs.PathError:
		x.Path = scriptPathOf(sp, ap, x.Path)
	case *os.LinkError:
		x.Old = scriptPathOf(sp, ap, x.Old)
		x.New = scriptPathOf(sp, ap, x.New)
	}
	return err
}

// fsTempDir resolves the dir argument of MkdirTemp/CreateTemp: an empty
// dir means os.TempDir() — only allowed when the engine is unrestricted
// (a temp dir outside the roots could otherwise anchor escaped writes).
func fsTempDir(env bind.Env, dir string) (string, error) {
	if dir == "" {
		if len(env.AllowedRoots()) == 0 {
			return "", nil
		}
		return "", errors.New("dir must name a directory inside the allowed roots")
	}
	return env.FsPath(dir)
}

// cwdAbs anchors a script path at the virtual cwd without a roots check —
// for pure path math (filepath.Abs/Rel) where no filesystem is touched.
func (e *Engine) cwdAbs(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(e.cwd, p)
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
