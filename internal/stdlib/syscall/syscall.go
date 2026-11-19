package syscall

import (
	"syscall"

	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
)

// Install binds the portable syscall surface (signals, errnos, Getpid);
// syscall's own sources need unsafe layout, so scripts get host values.
func Install(bind stdlib.Bind, h stdlib.Helpers) {
	// syscall's sources need unsafe layout (route_bsd's Offsetof) in
	// init. Bind the portable surface tools reach for — signals and
	// common errnos — as host values.
	bind("syscall", map[string]runtime.Value{
		"SIGINT":  &runtime.GoValue{V: syscall.SIGINT},
		"SIGQUIT": &runtime.GoValue{V: syscall.SIGQUIT},
		"SIGTERM": &runtime.GoValue{V: syscall.SIGTERM},
		"SIGKILL": &runtime.GoValue{V: syscall.SIGKILL},
		"SIGHUP":  &runtime.GoValue{V: syscall.SIGHUP},
		"ENOENT":  &runtime.GoValue{V: syscall.ENOENT},
		"EEXIST":  &runtime.GoValue{V: syscall.EEXIST},
		"EINTR":   &runtime.GoValue{V: syscall.EINTR},
		"EPIPE":   &runtime.GoValue{V: syscall.EPIPE},
		"EACCES":  &runtime.GoValue{V: syscall.EACCES},
		"ENOTDIR": &runtime.GoValue{V: syscall.ENOTDIR},
		"EISDIR":  &runtime.GoValue{V: syscall.EISDIR},
		"Getpid":  h.Fn("syscall.Getpid", func(a []any) (any, error) { return int(syscall.Getpid()), nil }),
	})
}
