package app

// Code is a defined type with no enum constants — the type name only
// appears in value position, which a text scan could mistake for a
// typed spec.
type Code int

const (
	// the inferred type is int — `Code(...)` after '=' types the
	// constant Code, so the outer conversion keeps this a non-member.
	DefaultCode  = int(Code(200))
	FallbackCode = 500 // untyped
)

// Rhythm is a defined type whose const block types its specs as Level:
// inheritance makes Beat a Level, not a Rhythm.
type Rhythm int

const (
	Cadence Level = "4/4"
	Beat
)
