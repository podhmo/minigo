package app

// Code is a defined type with no enum constants — the type name only
// appears in value position, which a text scan could mistake for a
// typed spec.
type Code int

const (
	DefaultCode  = Code(200) // Code after '=', not a typed spec
	FallbackCode = 500       // untyped
)

// Rhythm is a defined type whose const block types its specs as Level:
// inheritance makes Beat a Level, not a Rhythm.
type Rhythm int

const (
	Cadence Level = "4/4"
	Beat
)
