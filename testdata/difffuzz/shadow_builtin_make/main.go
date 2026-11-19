package main

// a package-level func named `make` shadows the builtin — `make(1)`
// calls it, and `1` is a value argument, not a type expression.
func make(x int) int { return x + 40 }

// same for `new`.
func new() int { return 7 }

var X = make(1)
var Y = new()

func main() {
	println(X, Y)
	// the builtins are still reachable... they are not — a declaration
	// shadows the name package-wide. So exercise the shadow only.
}
