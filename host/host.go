// Package host is the minigo host-extension stub package (plan
// docs/sketch/plan-minigo-vm.md §11): a real, importable Go package whose
// members name the interpreter's host-capability surface. Scripts keep
// working with gopls/gofmt/goimports because the stubs are ordinary Go
// declarations; the minigo engine intercepts calls and never executes
// these bodies — an intrinsic does the work instead.
//
// Scripts may import this package by its module path
// ("github.com/podhmo/minigo/host") or by the canonical
// "minigo.dev/host" — the engine binds both paths to the same intrinsics.
//
// Under a restricted engine (WithAllowedRoots), the
// environment/filesystem/argv surface is unbound; Exit is always a trap
// in every mode since an interpreted program must never terminate the
// host process.
package host

// Getenv returns the value of the named environment variable.
func Getenv(key string) string { panic("minigo intrinsic") }

// Environ returns all environment variables as "key=value" strings.
func Environ() []string { panic("minigo intrinsic") }

// Args returns the host command-line arguments, like os.Args.
func Args() []string { panic("minigo intrinsic") }

// Hostname returns the host name reported by the kernel.
func Hostname() (string, error) { panic("minigo intrinsic") }

// Getwd returns the current working directory.
func Getwd() (string, error) { panic("minigo intrinsic") }

// Exit requests process termination. Under minigo it always traps: an
// interpreted program must never terminate the host process.
func Exit(code int) { panic("minigo intrinsic") }
