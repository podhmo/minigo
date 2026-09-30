package convfooa

// Foo and convfoob.Foo share the local spelling "Foo" but are different
// declarations — []Foo spelled here and there must not collide.
type Foo struct{ X int }

// S spells "[]Foo" in this package — identical text, different identity.
type S []Foo
