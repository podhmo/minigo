package main

// init-order through function bodies: `var x = f()` must wait for every
// package-level name f (transitively) reads, not just names in x's own
// expression.

var x = f()

var z = 7 // source order after x, but f reads it transitively via g

func f() int { return g() * 2 }

func g() int { return z }

func Answer() int { return x }
