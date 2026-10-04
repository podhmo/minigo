// Package meta is a leaf package reached only with -deps: visited,
// scanned, and left untouched — nothing here matches a rule.
package meta

// Label is a label (no constants: not an enum).
type Label struct {
	Name string
}

// Inner shares its name with app.Inner but carries no required tags —
// only the resolved package decides, not the bare name.
type Inner struct {
	V string
}

// Plain is another leaf with nothing to generate.
type Plain struct{ X int }

// Untyped constants — not specs of a type.
const (
	LabelKindA = "a"
	LabelKindB = "b"
)
