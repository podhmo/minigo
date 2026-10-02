//go:build go1.27

package define

import (
	"context"

	"github.com/podhmo/minigo/examples/convert-define/model"
)

// Convert defines a mapping that requires a custom conversion function for a specific field.
// This is used when the default assignment or a global `Rule` is not sufficient.
// The converter must have the signature
// `func(ctx context.Context, ec *model.ErrorCollector, src SrcType) DstType`,
// enforced by the type parameters.
// Dotted field paths are supported like in `Map`.
//
// Example: `c.Convert(dst.Contact, src.ContactInfo, funcs.ConvertSrcContactToDstContact)`
func (c *Config) Convert[Dst, Src any](
	dstField Dst, srcField Src,
	convertFunc func(ctx context.Context, ec *model.ErrorCollector, src Src) Dst,
) {
	// This is a stub function for the parser.
}

// Compute defines a mapping for a destination field that is computed from an expression.
// The expression can be a function call or any other valid Go expression whose
// result type matches the destination field.
// The destination may be a dotted field path; pointer intermediates are
// nil-initialised before the write.
//
// Example: `c.Compute(dst.FullName, funcs.MakeFullName(src.FirstName, src.LastName))`
func (c *Config) Compute[T any](
	dstField T,
	computed T,
) {
	// This is a stub function for the parser.
}
