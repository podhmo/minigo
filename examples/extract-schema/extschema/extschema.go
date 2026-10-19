// Package extschema defines the configuration shared by extract-schema plugins.
//
// Plugins receive a [Config] in an exported func(*Config) hook. Use
// [Config.Override], [Config.DescField] and [Config.DescDecl] to register
// schema fragments and description filters.
package extschema

// Config holds a plugin's schema overrides and description filters.
// The extract-schema walk reads it after invoking the plugin hooks.
type Config struct {
	// Overrides maps "pkgpath.Name" to a schema fragment consulted
	// before the default named-type handling.
	Overrides map[string]any
	// DescFields filters every field description — the field
	// reference is passed (narrow it via reflect; a static
	// *inspect.Field assertion traps on cross-package type identity).
	DescFields []func(any, string) string
	// DescDecls filters every type description the same way.
	DescDecls []func(any, string) string
}

// Override registers one symbol's schema fragment. Fragments are
// copied on use, so a shared table can't be mutated by a caller.
func (c *Config) Override(symbolID string, fragment map[string]any) {
	if c.Overrides == nil {
		c.Overrides = map[string]any{}
	}
	c.Overrides[symbolID] = fragment
}

// DescField installs a field-description filter.
func (c *Config) DescField(fn func(any, string) string) {
	c.DescFields = append(c.DescFields, fn)
}

// DescDecl installs a type-description filter.
func (c *Config) DescDecl(fn func(any, string) string) {
	c.DescDecls = append(c.DescDecls, fn)
}
