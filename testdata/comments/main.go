// Package comments is the introspection subject for the
// comment-enumeration tests: doc comments, free comments,
// directive-shaped comments, and block comments.
package comments

// Documented is a documented function — its doc comment is attached.
func Documented() int { // a trailing comment on the signature line
	// swagger:route GET /x — a free comment a doc scan cannot reach
	x := 1 // a trailing comment inside a body is free too

	//go:generate echo directive — directive-shaped text stays visible
	return x
}

// a floating comment between decls — no declaration claims it

// Bye is a second documented function.
func Bye() string { return "bye" }

var V = /* an inline block comment */ 1
