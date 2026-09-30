//go:build codegen

// A plan-minigo-vm.md §12 acceptance fixture: every import below exercises
// one clause of the special-form contract.
//
//   - the `define` package is never located or parsed by the runtime —
//     SPECIAL_CALL dispatches on canonical SymbolID from the import table;
//   - dispatch is by canonical identity, not local name: the aliased `d`
//     still compiles to SPECIAL_CALL;
//   - a special call fires only when the VM reaches it: firing the dead
//     branch would register a second rule, and even materializing the
//     import would trip the resolver spy;
//   - arguments arrive quoted: convutil/source/destination are read by the
//     host goscan.Scanner inside the handler, never by the interpreter.
//
// The file is itself statically valid Go — the DSL property §12.1 relies
// on (gopls resolves, imports are real).

package main

import (
	"example.com/plan/bogus" // only quoted inside a dead branch below
	"example.com/plan/convutil"
	"example.com/plan/destination"
	"example.com/plan/source"

	d "github.com/podhmo/minigo/examples/convert-define/define"
)

func main() {
	d.Rule(convutil.TimeToString)

	if false {
		d.Rule(bogus.Nope) // never fires: would register a second rule
	}

	d.Convert(func(c *d.Config, dst *destination.DstUser, src *source.SrcUser) {
		c.Map(dst.UserID, src.ID)
	})
}
