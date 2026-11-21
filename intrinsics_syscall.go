package minigo

import (
	"syscall"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installSyscall(h *hostHelpers) {
	// syscall's sources need unsafe layout (route_bsd's Offsetof) in
	// init. Bind the portable surface tools reach for — signals and
	// common errnos — as host values.
	e.Bind("syscall", map[string]runtime.Value{
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
		"Getpid":  h.fn("syscall.Getpid", func(a []any) (any, error) { return int(syscall.Getpid()), nil }),
	})
}
