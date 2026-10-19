// Package define declares conversion rules for the convert-define tool.
//
// Use [Convert] with a function literal and [Config] methods for field
// exceptions; [Rule] registers reusable conversions. These declarations
// are interpreted by convert-define, not called directly from compiled Go.
package define

import (
	"context"

	"github.com/podhmo/minigo/examples/convert-define/model"
)

// Mapping is a placeholder for mapping configuration. It is not used directly.
type Mapping struct{}

// Config defines field exceptions inside a [Convert] mapping function.
// Use [Config.Map], [Config.Convert] or [Config.Compute].
type Config struct{}

// Convert declares a struct conversion. mapFunc must be a function literal;
// its body supplies exceptions to automatic field matching through [Config].
func Convert[Dst, Src any](mapFunc func(c *Config, dst *Dst, src *Src)) {
	// This is a stub function for the parser.
}

// Rule registers a reusable type conversion. customFunc must be a function
// identifier, for example define.Rule(convutil.TimeToString).
func Rule[Src, Dst any](customFunc func(ctx context.Context, ec *model.ErrorCollector, src Src) Dst) {
	// This is a stub function for the parser.
}

// Map maps source and destination fields, for example c.Map(dst.UserID, src.ID).
// Dotted paths are supported; pointer intermediates are nil-guarded on read
// and initialized on write. Different field types can use registered
// conversion pairs, including element-wise slice conversion.
func (c *Config) Map(dstField any, srcField any) {
	// This is a stub function for the parser.
}
