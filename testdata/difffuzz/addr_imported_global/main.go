package main

import (
	"fmt"

	"github.com/podhmo/minigo/testdata/difffuzz/addr_imported_global/inner"
)

// &pkg.V is the imported global's storage: it binds a typed *T param,
// compares equal to the package's own &V (encoding/json/jsontext's
// Export(&internal.AllowInternalUse) handshake) and aliases writes both
// ways.

func get(p *inner.Full) int { return p.X }

func main() {
	p := &inner.F
	p.X = 5
	inner.Bump()
	n := &inner.N
	*n += 10
	inner.Bump()
	fmt.Println(get(&inner.F), inner.F.X, p.X, inner.N, *n)
	fmt.Println(inner.IsAllow(&inner.Allow), &inner.F == p)
}
