//go:build codegen

package main

// Fixture for M0-6 quoted-Go pins: "example.com/define" never exists on
// disk — it is only a registered special form. "other.com/define" is a
// normally bound package in the tests.
import (
	d "example.com/define" // aliased import
	"other.com/define"
)

func main() {
	d.Convert(func(c *d.Config) {
		c.Map(1, 2)
	})
}

// aliasCall dispatches through the alias: importRef(d) resolves to
// example.com/define regardless of the alias spelling.
func aliasCall() {
	d.Convert(func(c *d.Config) {
		c.Map(1, 2)
	})
}

// sameSpelling calls a member of a DIFFERENT package whose last path
// element is also "define": it must reach the bound package, not the
// special form.
func sameSpelling() int {
	return define.OtherConvert()
}

type localT struct{ v int }

func (l localT) Convert(n int) int { return n * l.v }

// shadowed calls d.Convert where d is a local variable shadowing the
// import alias — Go resolves d to the local, so this is a method call,
// not a special form.
func shadowed() int {
	d := localT{v: 10}
	return d.Convert(5)
}
