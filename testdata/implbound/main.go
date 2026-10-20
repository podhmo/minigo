// Package implbound embeds host-bound interfaces in local ones so
// inspect.Implementers can be checked against them: fmt.Stringer's
// binding carries its String() string spec, io.Reader's only the
// method name, and strings.Builder is no interface at all.
package implbound

import (
	"fmt"
	"io"
	"strings"
)

// S requires exactly what fmt.Stringer requires.
type S interface{ fmt.Stringer }

// Mixed requires String() string and Name() string.
type Mixed interface {
	fmt.Stringer
	Name() string
}

// StrAlias is fmt.Stringer under a local name.
type StrAlias = fmt.Stringer

// ViaAlias reaches fmt.Stringer through the alias.
type ViaAlias interface{ StrAlias }

// R embeds a bound interface whose method set minigo cannot read.
type R interface{ io.Reader }

// MixedR has a readable local method next to the unreadable embed.
type MixedR interface {
	Name() string
	io.Reader
}

// B embeds a bound non-interface type: a constraint term.
type B interface{ strings.Builder }

// Str implements fmt.Stringer.
type Str struct{}

func (Str) String() string { return "" }

// PStr implements it through a pointer receiver.
type PStr struct{}

func (*PStr) String() string { return "" }

// Both implements Mixed.
type Both struct{}

func (Both) String() string { return "" }
func (Both) Name() string   { return "" }

// NameOnly lacks String.
type NameOnly struct{}

func (NameOnly) Name() string { return "" }

// BadStr has String with the wrong result.
type BadStr struct{}

func (BadStr) String() int { return 0 }

// None has no methods.
type None struct{}

// Wrap gets String promoted from Str.
type Wrap struct{ Str }

// EmbStringer gets String promoted from an embedded fmt.Stringer field.
type EmbStringer struct{ fmt.Stringer }

// Own spells the requirement locally.
type Own interface{ String() string }
