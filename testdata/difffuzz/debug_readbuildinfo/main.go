package main

import (
	"fmt"
	"runtime/debug"
)

func main() {
	bi, ok := debug.ReadBuildInfo()
	fmt.Println(ok, bi.Path, bi.Main.Path, bi.Main.Version)
}
