// Package extschema is the shared contract between the extract-schema
// walk and its plugins — the counterpart of gen-sync's scanx/: plugins
// receive a *Config and register settings on it, instead of spelling
// untyped map keys.
package extschema

// Config is the registrar a plugin's Configure receives. The walk
// reads Overrides/DescFields/DescDecls afterward; plugins should
// prefer the methods over writing the fields directly so future
// validation lands in one place.
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
