package main

import (
	inithelper "github.com/podhmo/minigo/testdata/inithelper"
	"github.com/podhmo/minigo/testdata/inithook"
	"github.com/podhmo/minigo/testdata/inithookset"
	"github.com/podhmo/minigo/testdata/inittable"
)

func ViaFunc() int { return inittable.Lookup() } // only func access -> init must run
func main()        {}

// Indirect: the init-built table is reached through another package's
// function that itself touches only func members of inittable.
func ViaIndirect() int { return inithelper.Get() }

// ViaHook: inithookset's init() writes into inithook before main reads
// inithook.End — inithook must be initialized before that write, or its
// own initializer resets End to nil afterwards (go/parser on Go 1.26
// reads scannerhooks.StringEnd the same way).
func ViaHook() int {
	var p struct{ s inithookset.S }
	p.s.Init(7)
	return inithook.End(&p.s)
}
