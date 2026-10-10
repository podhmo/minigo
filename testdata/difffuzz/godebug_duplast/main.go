package main

// gc cannot run this program — internal/* imports are std-only — so
// want.stdout is handwritten to gc's semantics: the runtime parses
// GODEBUG keeping the LAST entry per name (and testenv.SetGODEBUG's
// append relies on it).

import (
	"fmt"
	"internal/godebug"
	"internal/testenv"
	"os"
)

func main() {
	defer os.Setenv("GODEBUG", os.Getenv("GODEBUG")) // restore: the setenvs below hit the host env

	s := godebug.New("urlmaxqueryparams")
	os.Setenv("GODEBUG", "")
	fmt.Println("empty:", s.Value())
	os.Setenv("GODEBUG", "urlmaxqueryparams=1,urlmaxqueryparams=0")
	fmt.Println("dup:", s.Value())
	os.Setenv("GODEBUG", "a=1,urlmaxqueryparams=9,x=2")
	fmt.Println("mid:", s.Value())
	testenv.SetGODEBUG(nil, "urlmaxqueryparams=7")
	fmt.Println("override:", s.Value())
	fmt.Println("str:", s.String())
}
