//go:build codegen
// +build codegen

package main

import (
	"github.com/podhmo/minigo/examples/convert-define/convutil"
	"github.com/podhmo/minigo/examples/convert-define/define"
	"github.com/podhmo/minigo/examples/convert-define/sampledata/destination"
	"github.com/podhmo/minigo/examples/convert-define/sampledata/funcs"
	"github.com/podhmo/minigo/examples/convert-define/sampledata/source"
)

func main() {
	// Global rules
	define.Rule(convutil.TimeToString)
	define.Rule(convutil.PtrTimeToString)

	// User conversion with all mapping types
	define.Convert(func(c *define.Config, dst *destination.DstUser, src *source.SrcUser) {
		// Implicit: CreatedAt, UpdatedAt
		// Explicit Map: different names
		c.Map(dst.UserID, src.ID)
		// Explicit Convert: different names and type conversion
		c.Convert(dst.Contact, src.ContactInfo, funcs.ConvertSrcContactToDstContact)
		// Explicit Compute: computed value
		c.Compute(dst.FullName, funcs.MakeFullName(src.FirstName, src.LastName))
	})

	// Address conversion with only mapping
	define.Convert(func(c *define.Config, dst *destination.DstAddress, src *source.SrcAddress) {
		// Implicit: City -> CityName (should not be mapped automatically)
		// Explicit Map: different names
		c.Map(dst.FullStreet, src.Street)
		c.Map(dst.CityName, src.City)
	})
}
