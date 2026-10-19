// Package host gives minigo scripts access to the real host process environment.
//
// Scripts import "minigo.dev/host" or this module path and call [Getenv],
// [Environ], [Args], [Hostname] or [Getwd]. Do not call these functions from
// compiled Go. [Args] and [Getwd] bypass the engine's virtual argv and directory.
//
// With [github.com/podhmo/minigo.WithAllowedRoots], only [Exit] is bound.
// [Exit] always fails: scripts cannot terminate the host process.
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
