// Package meta is a leaf package reached only with -deps: visited,
// scanned, and left untouched — nothing here matches a rule.
package meta

// Label is a label (no constants: not an enum).
type Label struct {
	Name string
}

// Untyped constants — not specs of a type.
const (
	LabelKindA = "a"
	LabelKindB = "b"
)
