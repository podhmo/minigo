package osexec

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"

	intbind "github.com/podhmo/minigo/internal/bind"
	"github.com/podhmo/minigo/internal/stdlib"
	stdcontext "github.com/podhmo/minigo/internal/stdlib/context"
	"github.com/podhmo/minigo/runtime"
)

func Install(bind stdlib.Bind, h stdlib.Helpers, env intbind.Env) {
	// os/exec: spawning a subprocess escapes per-path confinement, so the
	// package is only bound for unrestricted engines. Commands default to
	// the engine's virtual cwd via cmd.Dir; scripts wire stdio through the
	// boxed os.Stdin/Stdout/Stderr handles.
	if len(env.AllowedRoots()) == 0 {
		bind("os/exec", map[string]runtime.Value{
			"Command": h.Fn("exec.Command", func(a []any) (any, error) {
				if len(a) == 0 {
					return nil, errors.New("exec.Command needs a name")
				}
				cmd := exec.Command(stdlib.Str(a[0]), stdlib.StrSlice(a[1:])...)
				cmd.Dir = env.Cwd()
				return &runtime.GoValue{V: cmd}, nil
			}, exec.Command),
			"CommandContext": h.Fn("exec.CommandContext", func(a []any) (any, error) {
				if len(a) < 2 {
					return nil, errors.New("exec.CommandContext needs a context and a name")
				}
				ctx, err := stdcontext.AsCtx(a[0])
				if err != nil {
					return nil, err
				}
				cmd := exec.CommandContext(ctx, stdlib.Str(a[1]), stdlib.StrSlice(a[2:])...)
				cmd.Dir = env.Cwd()
				return &runtime.GoValue{V: cmd}, nil
			}, exec.CommandContext),
			"LookPath": h.Fn1("exec.LookPath", func(a []any) (any, error) {
				name := stdlib.Str(a[0])
				// A separator-bearing relative name is checked against the
				// caller's cwd — here the engine's virtual one — but Go returns
				// the name in the shape it was given, so don't absolutize the
				// result.
				if strings.ContainsRune(name, '/') && !filepath.IsAbs(name) {
					if _, err := exec.LookPath(env.CwdAbs(name)); err != nil {
						return stdlib.RetErr2("", err)
					}
					return stdlib.RetErr2(name, nil)
				}
				return stdlib.RetErr2(exec.LookPath(name))
			}, exec.LookPath),
			"ErrNotFound":  &runtime.GoValue{V: exec.ErrNotFound},
			"ErrDot":       &runtime.GoValue{V: exec.ErrDot},
			"ErrWaitDelay": &runtime.GoValue{V: exec.ErrWaitDelay},
			// error types for errors.As / type asserts on LookPath's and
			// Run's results.
			"Error":     stdlib.HostType("os/exec.Error", func() any { return &exec.Error{} }),
			"ExitError": stdlib.HostType("os/exec.ExitError", func() any { return &exec.ExitError{} }),
		})
	}
}
