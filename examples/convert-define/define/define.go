// Package define provides the API for defining conversion rules.
// This package contains stub functions that are not meant to be executed directly,
// but are instead parsed by the `convert-define` tool to generate conversion code.
package define

import (
	"context"

	"github.com/podhmo/minigo/examples/convert-define/model"
)

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
func Convert[Dst, Src any](mapFunc func(c *Config, dst *Dst, src *Src)) {
	// This is a stub function for the parser.
}

// Rule defines a global, reusable conversion rule for a specific type-to-type conversion.
// The parser infers the source and destination types from the function's signature.
// For example, `define.Rule(convutil.TimeToString)` where TimeToString is
// `func(ctx context.Context, ec *model.ErrorCollector, t time.Time) string`
// would establish a global rule for converting `time.Time` to `string`.
//
// The customFunc parameter is a function identifier (e.g., `convutil.TimeToString`).
func Rule[Src, Dst any](customFunc func(ctx context.Context, ec *model.ErrorCollector, src Src) Dst) {
	// This is a stub function for the parser.
}

// Map defines a mapping between two fields with different names.
// This is only necessary when the source and destination field names do not match.
//
// Both arguments may be dotted field paths through nested structs:
// `c.Map(dst.Inner.ID, src.ID)` writes a leaf of the nested destination,
// `c.Map(dst.Flat, src.In.Value)` reads through a nested source field.
// Pointer intermediates are nil-guarded on read and nil-initialised on write.
//
// The field types do not have to be identical — the generator maps through
// registered conversion pairs element-wise (e.g. `[]SrcItem` to `[]DstItem`),
// which cannot be expressed in the signature, so they stay `any`.
//
// Example: `c.Map(dst.UserID, src.ID)`
func (c *Config) Map(dstField any, srcField any) {
	// This is a stub function for the parser.
}
