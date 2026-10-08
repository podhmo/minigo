// Package consttheninit feeds the const-then-run sequence: a host read
// of C binds it without init, and a later in-VM member access must still
// complete the initializer — Go guarantees an imported package is
// initialized before its values serve.
package consttheninit

// Inited records that the package initializer ran.
var Inited = false

func init() { Inited = true }

// C is a plain constant — a host read binds it without init.
const C = 7
