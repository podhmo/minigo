//go:build codegen

package main

// A convert-define-style DSL file: every import below is unresolvable on
// disk — the whole point is that quoted special calls never materialize
// what they reference.

import (
	"example.com/convutil"
	d "example.com/define"
	"example.com/destination"
	"example.com/source"
)

func main() {
	d.Rule(convutil.TimeToString)
	d.Convert(func(c *d.Config, dst *destination.DstUser, src *source.SrcUser) {
		c.Map(dst.UserID, src.ID)
		c.Convert(dst.Contact, src.ContactInfo, convutil.ConvertContact)
		c.Compute(dst.FullName, convutil.MakeFullName(src.FirstName, src.LastName))
	})
}
