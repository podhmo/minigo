//go:build !go1.27

package define

// Convert defines a mapping that requires a custom conversion function for a specific field.
// This is used when the default assignment or a global `Rule` is not sufficient.
// The converter must have the signature
// `func(ctx context.Context, ec *model.ErrorCollector, src SrcType) DstType`.
// Dotted field paths are supported like in `Map`.
//
// On go1.27+ toolchains this method is generic and type-checks the field
// types against the converter signature; here the arguments stay `any`.
//
// Example: `c.Convert(dst.Contact, src.ContactInfo, funcs.ConvertSrcContactToDstContact)`
func (c *Config) Convert(
	dstField any, srcField any,
	convertFunc any,
) {
	// This is a stub function for the parser.
}

// Compute defines a mapping for a destination field that is computed from an expression.
// The expression can be a function call or any other valid Go expression.
// The destination may be a dotted field path; pointer intermediates are
// nil-initialised before the write.
//
// On go1.27+ toolchains this method is generic and requires the expression's
// result type to match the destination field; here the arguments stay `any`.
//
// Example: `c.Compute(dst.FullName, funcs.MakeFullName(src.FirstName, src.LastName))`
func (c *Config) Compute(
	dstField any,
	computeFunc any,
) {
	// This is a stub function for the parser.
}
