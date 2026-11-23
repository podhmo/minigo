// Package infercallee is inferpkg's cross-package call target.
package infercallee

// T is a membered result type for New.
type T struct{ F int }

// M is a value-receiver method on T.
func (t T) M() int { return t.F }

// New returns a T — `var x = infercallee.New()` infers T.
func New() T { return T{F: 1} }
