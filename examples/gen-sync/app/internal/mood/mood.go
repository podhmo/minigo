// Package mood is a leaf package reached only with -deps.
package mood

// Mood is a mood.
type Mood int

const (
	Happy Mood = iota
	Sad
)

// Marked carries a required tag: app's Remote reaches it through the
// subtree even without -deps (exploration scope), while mood's own
// file only earns a managed block when -deps makes it a sync target.
type Marked struct {
	Label string `required:"true"`
}

// Signal implements app's Envelope from another package in the subtree:
// the variant collection sees it through the import closure either way,
// and -deps turns this file into a sync target too.
type Signal struct{ At int64 }

func (Signal) Discriminator() string { return "signal" }
