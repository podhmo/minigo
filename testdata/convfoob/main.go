package convfoob

// Foo is a different declaration than convfooa.Foo — same local name,
// different package.
type Foo struct{ X int }

// S spells "[]Foo" in this package — identical text, different identity.
type S []Foo
