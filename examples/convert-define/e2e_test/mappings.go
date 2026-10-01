//go:build codegen
// +build codegen

package e2e

import (
	"github.com/podhmo/minigo/examples/convert-define/convutil"
	"github.com/podhmo/minigo/examples/convert-define/define"
	"github.com/podhmo/minigo/examples/convert-define/sampledata/destination"
	"github.com/podhmo/minigo/examples/convert-define/sampledata/funcs"
	"github.com/podhmo/minigo/examples/convert-define/sampledata/source"
)

func main() {
	define.Rule(convutil.TimeToString)
	define.Rule(convutil.PtrTimeToString)

	define.Convert(func(c *define.Config, dst *destination.DstUser, src *source.SrcUser) {
		c.Convert(dst.UserID, src.ID, funcs.UserIDToString)
		c.Convert(dst.Contact, src.ContactInfo, funcs.ConvertSrcContactToDstContact)
		c.Compute(dst.FullName, funcs.MakeFullName(src.FirstName, src.LastName))
	})

	define.Convert(func(c *define.Config, dst *destination.DstAddress, src *source.SrcAddress) {
		c.Map(dst.FullStreet, src.Street)
		c.Map(dst.CityName, src.City)
	})

	define.Convert(func(c *define.Config, dst *destination.DstInternalDetail, src *source.SrcInternalDetail) {
		c.Map(dst.ItemCode, src.Code)
		c.Convert(dst.LocalizedDesc, src.Description, funcs.Translate)
	})

	define.Convert(func(c *define.Config, dst *destination.DstOrder, src *source.SrcOrder) {
		c.Map(dst.ID, src.OrderID)
		c.Map(dst.TotalAmount, src.Amount)
		c.Map(dst.LineItems, src.Items)
	})

	define.Convert(func(c *define.Config, dst *destination.DstItem, src *source.SrcItem) {
		c.Map(dst.ProductCode, src.SKU)
		c.Map(dst.Count, src.Quantity)
	})

	define.Convert(func(c *define.Config, dst *destination.ComplexTarget, src *source.ComplexSource) {})
	define.Convert(func(c *define.Config, dst *destination.SubTarget, src *source.SubSource) {})

	define.Convert(func(c *define.Config, dst *destination.TargetWithMap, src *source.SourceWithMap) {})

	// Nested field paths: a leaf inside a nested dst struct, a leaf
	// read through a nested src struct (value and pointer receivers),
	// and a leaf written through a nil-able dst pointer.
	define.Convert(func(c *define.Config, dst *destination.DstNested, src *source.SrcNested) {
		c.Map(dst.Inner.ID, src.ID)
		c.Map(dst.Flat, src.Inner.Value)
		c.Map(dst.Tag, src.PIn.Value)
		c.Map(dst.PIn.Value, src.Name)
	})
}
