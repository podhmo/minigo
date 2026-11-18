package minigo

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installOsExec(h *hostHelpers) {
	// os/exec: spawning a subprocess escapes per-path confinement, so the
	// package is only bound for unrestricted engines. Commands default to
	// the engine's virtual cwd via cmd.Dir; scripts wire stdio through the
	// boxed os.Stdin/Stdout/Stderr handles.
	if len(h.env.AllowedRoots()) == 0 {
		e.Bind("os/exec", map[string]runtime.Value{
			"Command": h.fn("exec.Command", func(a []any) (any, error) {
				if len(a) == 0 {
					return nil, errors.New("exec.Command needs a name")
				}
				cmd := exec.Command(str(a[0]), strSlice(a[1:])...)
				cmd.Dir = h.env.Cwd()
				return &runtime.GoValue{V: cmd}, nil
			}),
			"CommandContext": h.fn("exec.CommandContext", func(a []any) (any, error) {
				if len(a) < 2 {
					return nil, errors.New("exec.CommandContext needs a context and a name")
				}
				ctx, err := asCtx(a[0])
				if err != nil {
					return nil, err
				}
				cmd := exec.CommandContext(ctx, str(a[1]), strSlice(a[2:])...)
				cmd.Dir = h.env.Cwd()
				return &runtime.GoValue{V: cmd}, nil
			}),
			"LookPath": h.fn1("exec.LookPath", func(a []any) (any, error) {
				name := str(a[0])
				// A separator-bearing relative name is checked against the
				// caller's cwd — here the engine's virtual one — but Go returns
				// the name in the shape it was given, so don't absolutize the
				// result.
				if strings.ContainsRune(name, '/') && !filepath.IsAbs(name) {
					if _, err := exec.LookPath(h.env.CwdAbs(name)); err != nil {
						return retErr2("", err)
					}
					return retErr2(name, nil)
				}
				return retErr2(exec.LookPath(name))
			}),
			"ErrNotFound":  &runtime.GoValue{V: exec.ErrNotFound},
			"ErrDot":       &runtime.GoValue{V: exec.ErrDot},
			"ErrWaitDelay": &runtime.GoValue{V: exec.ErrWaitDelay},
			// error types for errors.As / type asserts on LookPath's and
			// Run's results.
			"Error":     hostType("os/exec.Error", func() any { return &exec.Error{} }),
			"ExitError": hostType("os/exec.ExitError", func() any { return &exec.ExitError{} }),
		})
	}
}
