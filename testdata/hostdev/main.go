package main

// hostdev exercises the canonical minigo.dev/host path from the plan:
// the engine binds it to the same intrinsics as the in-repo stub path.
import "minigo.dev/host"

func EnvPath() string { return host.Getenv("PATH") }

func main() {}
