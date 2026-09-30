package main

import (
	inithelper "github.com/podhmo/minigo/testdata/inithelper"
	"github.com/podhmo/minigo/testdata/inittable"
)

func ViaFunc() int { return inittable.Lookup() } // only func access -> init must run
func main()        {}

// Indirect: the init-built table is reached through another package's
// function that itself touches only func members of inittable.
func ViaIndirect() int { return inithelper.Get() }
