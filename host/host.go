// Package host gives minigo scripts access to the real host process
// environment, as opposed to the engine's view of it.
//
// Import it as "minigo.dev/host" or by its module path
// "github.com/podhmo/minigo/host"; both name the same functions. The
// declarations are stubs so that gopls, gofmt and goimports work on
// scripts: inside minigo the engine handles each call and the bodies never
// run. Do not call them from compiled Go.
//
//	import "minigo.dev/host"
//
//	func Main() string {
//		return host.Getenv("HOME")
//	}
//
// When the engine is restricted with
// [github.com/podhmo/minigo.WithAllowedRoots], only [Exit] is bound, so a
// script using any other member fails.
package host

// Getenv returns the value of the named environment variable of the host
// process.
func Getenv(key string) string { panic("minigo intrinsic") }

// Environ returns the host process environment as "key=value" strings.
func Environ() []string { panic("minigo intrinsic") }

// Args returns the host process's command-line arguments. Unlike os.Args
// in a script, it ignores [github.com/podhmo/minigo.WithArgs].
func Args() []string { panic("minigo intrinsic") }

// Hostname returns the host name reported by the kernel.
func Hostname() (string, error) { panic("minigo intrinsic") }

// Getwd returns the host process's working directory. Unlike os.Getwd in
// a script, it ignores [github.com/podhmo/minigo.WithWorkingDir].
func Getwd() (string, error) { panic("minigo intrinsic") }

// Exit always fails with a runtime error: a script cannot terminate the
// host process. Return from the entry function instead.
func Exit(code int) { panic("minigo intrinsic") }
