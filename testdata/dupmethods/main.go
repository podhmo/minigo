// Package dupmethods pins duplicate same-name method declarations:
// illegal Go (go build rejects it), but the syntax index still records
// every written decl, so introspection must not silently drop them.
package dupmethods

// T is the receiver of the duplicated decl.
type T struct{}

// Dup is the first declaration — its doc must stay reachable.
func (T) Dup() {}

// Dup is the second declaration of the same method name.
func (T) Dup() {}

// Other is a separate method.
func (T) Other() {}
