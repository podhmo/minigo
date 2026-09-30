// Package define provides the API for defining conversion rules.
// This package contains stub functions that are not meant to be executed directly,
// but are instead parsed by the `convert-define` tool to generate conversion code.
package define

// Mapping is a placeholder for mapping configuration. It is not used directly.
type Mapping struct{}

// Config is the configurator for defining field-level mapping exceptions.
// An instance of Config is passed to the mapping function in `Convert`.
type Config struct{}

// Convert defines a conversion between two struct types by specifying custom mapping logic.
// The source and destination types are inferred from the signature of the mapFunc.
//
// The mapFunc parameter is a function literal with the specific signature:
//
//	func(c *Config, dst *DestinationType, src *SourceType)
//
// Inside this function, you define exceptions to the default field mapping behavior.
func Convert(mapFunc any) {
	// This is a stub function for the parser.
}

// Rule defines a global, reusable conversion rule for a specific type-to-type conversion.
// The parser infers the source and destination types from the function's signature.
// For example, `define.Rule(convutil.TimeToString)` where TimeToString is `func(t time.Time) string`
// would establish a global rule for converting `time.Time` to `string`.
//
// The customFunc parameter is a function identifier (e.g., `convutil.TimeToString`).
func Rule(customFunc any) {
	// This is a stub function for the parser.
}

// Map defines a mapping between two fields with different names.
// This is only necessary when the source and destination field names do not match.
//
// Example: `c.Map(dst.UserID, src.ID)`
func (c *Config) Map(dstField any, srcField any) {
	// This is a stub function for the parser.
}

// Convert defines a mapping that requires a custom conversion function for a specific field.
// This is used when the default assignment or a global `Rule` is not sufficient.
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
//
// Example: `c.Compute(dst.FullName, funcs.MakeFullName(src.FirstName, src.LastName))`
func (c *Config) Compute(
	dstField any,
	computeFunc any,
) {
	// This is a stub function for the parser.
}
