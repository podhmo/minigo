package main

import "fmt"

// new(alias) for an alias of an unnamed array binds a *[N]T slot —
// encoding/json/v2's `xd.StringCache = new(stringCache)` with
// `type stringCache = [256]string`.

type cache = [4]string

type state struct{ C *[4]string }

func main() {
	var s state
	s.C = new(cache)
	s.C[1] = "x"
	var p *[4]string = new(cache)
	p[3] = "y"
	fmt.Println(s.C[1], len(p), p[3])
}
