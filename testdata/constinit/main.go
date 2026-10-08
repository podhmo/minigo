// Package constinit feeds const materialization tests: every const
// binds without running the package's initializers.
package constinit

func mark() int { panic("BOOM: constinit initialized") }

// Touched forces an expensive init that only runs when the package is
// actually initialized (first non-const member access).
var Touched = mark()

const (
	// Lit is a plain literal const.
	Lit = "cloudwatch"
	// Base feeds Derived's initializer.
	Base = 1
	// Derived reads another const in the same package.
	Derived = Base + 1
)

const (
	// Iota0 starts an iota run; Iota1/Iota2 inherit the expression.
	Iota0 = iota
	Iota1
	Iota2
)

// Typed carries a declared type on the spec.
const Typed int64 = 7
