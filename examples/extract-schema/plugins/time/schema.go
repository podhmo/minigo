// Package timeschema is the example extract-schema plugin: a script
// package providing Config() — the single entry point the walk calls
// per plugin dir. Here it reads time.Time and time.Duration as the
// strings their JSON marshaling produces.
package timeschema

// Config returns the plugin's settings: an "overrides" table of
// symbolID -> schema-fragment consulted before the default named-type
// walk. A plugin may also set "descField"/"descDecl" to
// func(any, string) string description filters (see plugins/desc).
func Config() map[string]any {
	return map[string]any{
		"overrides": map[string]any{
			"time.Time": map[string]any{
				"type":        "string",
				"format":      "date-time",
				"description": "RFC 3339 timestamp (time.Time)",
			},
			"time.Duration": map[string]any{
				"type":        "integer",
				"description": "nanoseconds (time.Duration)",
			},
		},
	}
}
