// Package enumcode is the override plugin for the ,string tag option
// test: it registers fragments whose enum/const members and numeric
// bounds must rewire when a field marshals scalars as quoted strings.
package enumcode

import "github.com/podhmo/minigo/examples/extract-schema/extschema"

// Install registers this plugin's overrides on the shared config —
// symbolID -> schema-fragment consulted before the default named-type
// walk.
func Install(c *extschema.Config) {
	c.Override("github.com/podhmo/minigo/examples/extract-schema/app.Code", map[string]any{
		"type":             "integer",
		"enum":             []any{1, 2},
		"minimum":          0,
		"maximum":          9,
		"exclusiveMinimum": 0,
		"exclusiveMaximum": 9,
		"multipleOf":       1,
	})
	c.Override("github.com/podhmo/minigo/examples/extract-schema/app.Ratio", map[string]any{
		"type":  "number",
		"const": 1.5,
	})
	c.Override("github.com/podhmo/minigo/examples/extract-schema/app.Label", map[string]any{
		"type": "string",
		"enum": []any{"a", "b"},
	})
}
