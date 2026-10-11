// Package timeschema is the example extract-schema plugin: a script
// package providing Configure — the single entry point the walk calls
// per plugin dir. Here it reads time.Time and time.Duration as the
// strings their JSON marshaling produces.
package timeschema

import "github.com/podhmo/minigo/examples/extract-schema/extschema"

// Install registers this plugin's overrides on the shared config —
// symbolID -> schema-fragment consulted before the default named-type
// walk. A plugin may also install desc filters (see plugins/desc).
func Install(c *extschema.Config) {
	c.Override("time.Time", map[string]any{
		"type":        "string",
		"format":      "date-time",
		"description": "RFC 3339 timestamp (time.Time)",
	})
	c.Override("time.Duration", map[string]any{
		"type":        "integer",
		"description": "nanoseconds (time.Duration)",
	})
}

// Validate shares the entry signature except it returns an error — the
// walk must not treat it as an entry point (asserting func(*Config)
// on it would trap), so it exists as the regression pin.
func Validate(c *extschema.Config) error {
	return nil
}
