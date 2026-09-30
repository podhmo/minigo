//go:build codegen
// +build codegen

package e2e

import (
	"github.com/podhmo/go-scan/examples/convert/convutil"
	"github.com/podhmo/go-scan/examples/convert/sampledata/destination"
	"github.com/podhmo/go-scan/examples/convert/sampledata/funcs"
	"github.com/podhmo/go-scan/examples/convert/sampledata/source"
	"github.com/podhmo/minigo/examples/convert-define/define"
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
}
