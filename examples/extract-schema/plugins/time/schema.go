// Package timeschema is the example extract-schema plugin: a script
// package providing Overrides(), a symbolID -> schema-fragment table
// the host merges and hands to the walk. Here it reads time.Time and
// time.Duration as the strings their JSON marshaling produces.
package timeschema

// Overrides is the plugin entry point the host calls once per run.
func Overrides() map[string]any {
	return map[string]any{
		"time.Time": map[string]any{
			"type":        "string",
			"format":      "date-time",
			"description": "RFC 3339 timestamp (time.Time)",
		},
		"time.Duration": map[string]any{
			"type":        "integer",
			"description": "nanoseconds (time.Duration)",
		},
	}
}
