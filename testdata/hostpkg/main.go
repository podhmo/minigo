package main

// hostpkg exercises the in-repo stub path: the import is valid Go for
// gopls inside this module, but the engine binds the path to intrinsics
// so the panic("minigo intrinsic") stub bodies never run.
import "github.com/podhmo/minigo/host"

func EnvPath() string { return host.Getenv("PATH") }

func Argc() int { return len(host.Args()) }

func Host() string {
	name, err := host.Hostname()
	if err != nil {
		return "err"
	}
	return name
}

func Cwd() string {
	dir, err := host.Getwd()
	if err != nil {
		return "err"
	}
	if len(dir) == 0 {
		return "empty"
	}
	return "ok"
}

func Exit() { host.Exit(3) }

func main() {}
