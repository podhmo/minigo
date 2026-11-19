package host

import (
	"errors"
	"os"

	intbind "github.com/podhmo/minigo/internal/bind"
	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
)

func Install(bind stdlib.Bind, h stdlib.Helpers, env intbind.Env) {
	// host: the gopls-friendly stub-package surface (plan §11). Scripts may
	// spell the import either canonically ("minigo.dev/host") or via the
	// in-repo stub package ("github.com/podhmo/minigo/host",
	// which ships panic("minigo intrinsic") bodies) — both paths resolve
	// to this intrinsic table, so stub bodies never execute.
	hostpkg := map[string]runtime.Value{
		"Exit": h.Fn("host.Exit", func(a []any) (any, error) {
			return nil, errors.New("host.Exit is not supported: an interpreted program cannot terminate the host process")
		}),
	}

	if len(env.AllowedRoots()) == 0 {
		hostpkg["Getenv"] = h.Fn("host.Getenv", func(a []any) (any, error) { return os.Getenv(stdlib.Str(a[0])), nil })
		hostpkg["Environ"] = h.Fn("host.Environ", func(a []any) (any, error) { return stdlib.StrsSlice(os.Environ()), nil })
		hostpkg["Args"] = h.Fn("host.Args", func(a []any) (any, error) { return stdlib.StrsSlice(os.Args), nil })
		hostpkg["Hostname"] = h.Fn("host.Hostname", func(a []any) (any, error) {
			return stdlib.RetErr2(os.Hostname())
		})
		hostpkg["Getwd"] = h.Fn("host.Getwd", func(a []any) (any, error) {
			return stdlib.RetErr2(os.Getwd())
		})
	}

	for _, path := range []string{"minigo.dev/host", "github.com/podhmo/minigo/host"} {
		bind(path, hostpkg)
	}
}
