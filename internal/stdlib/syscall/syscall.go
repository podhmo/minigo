package syscall

import (
	"go/ast"
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
		"SIGUSR1": &runtime.GoValue{V: syscall.SIGUSR1},
		"SIGUSR2": &runtime.GoValue{V: syscall.SIGUSR2},
		"SIGPIPE": &runtime.GoValue{V: syscall.SIGPIPE},
		// Errno binds like time.Weekday — a named basic typedef whose
		// HostScalar makes conversions box host errnos that print via
		// their Error method.
		"Errno": &runtime.TypeDef{Name: "syscall.Errno", Kind: runtime.KindNamedBasic,
			Anon: ast.NewIdent("uintptr"), HostScalar: syscall.Errno(0)},
		"ENOENT":  &runtime.GoValue{V: syscall.ENOENT},
		"EEXIST":  &runtime.GoValue{V: syscall.EEXIST},
		"EINTR":   &runtime.GoValue{V: syscall.EINTR},
		"EPIPE":   &runtime.GoValue{V: syscall.EPIPE},
		"EACCES":  &runtime.GoValue{V: syscall.EACCES},
		"ENOTDIR": &runtime.GoValue{V: syscall.ENOTDIR},
		"EISDIR":  &runtime.GoValue{V: syscall.EISDIR},
		"EINVAL":  &runtime.GoValue{V: syscall.EINVAL},
		"EBADF":   &runtime.GoValue{V: syscall.EBADF},
		"EAGAIN":  &runtime.GoValue{V: syscall.EAGAIN},
		"EFAULT":  &runtime.GoValue{V: syscall.EFAULT},
		"ENODEV":  &runtime.GoValue{V: syscall.ENODEV},
		"ENOSPC":  &runtime.GoValue{V: syscall.ENOSPC},
		"EPERM":   &runtime.GoValue{V: syscall.EPERM},
		"ESRCH":   &runtime.GoValue{V: syscall.ESRCH},
		"EIO":     &runtime.GoValue{V: syscall.EIO},
		"ENXIO":   &runtime.GoValue{V: syscall.ENXIO},
		"ECHILD":  &runtime.GoValue{V: syscall.ECHILD},
		"E2BIG":   &runtime.GoValue{V: syscall.E2BIG},
		"ENOEXEC": &runtime.GoValue{V: syscall.ENOEXEC},
		"ENOMEM":  &runtime.GoValue{V: syscall.ENOMEM},
		"EBUSY":   &runtime.GoValue{V: syscall.EBUSY},
		"EXDEV":   &runtime.GoValue{V: syscall.EXDEV},
		"EROFS":   &runtime.GoValue{V: syscall.EROFS},
		"EMLINK":  &runtime.GoValue{V: syscall.EMLINK},
		"EDOM":    &runtime.GoValue{V: syscall.EDOM},
		"ERANGE":  &runtime.GoValue{V: syscall.ERANGE},
		"ENOSYS":  &runtime.GoValue{V: syscall.ENOSYS},
		"Getpid":  h.Fn("syscall.Getpid", func(a []any) (any, error) { return int(syscall.Getpid()), nil }, syscall.Getpid),
	})
}
