package main

// Good: the package compiles; only the illegal gotos trap when called.
func Good() int { return 7 }

// IntoBlock: `goto` cannot jump into a block — the label's open blocks
// must all be open at the goto. Compiles to a run-time trap.
func IntoBlock() int {
	goto in
	{
		x := 1
	in:
		return x
	}
	return 0
}

// OverDecl: `goto` cannot jump over a variable declaration that is in
// scope at the label.
func OverDecl() int {
	goto end
	y := 5
	_ = y
end:
	return 1
}

// Fine: a legal backward goto still works.
func Fine() int {
	n := 0
loop:
	n++
	if n < 3 {
		goto loop
	}
	return n // 3
}

// Shadow: the label resolves `x` to the inner declaration declared
// between the goto and the label — illegal even though an outer `x`
// was already visible at the goto (a name-only check would miss it).
func Shadow() int {
	x := 1
	if true {
		goto L
		x := 2
	L:
		return x
	}
}

func main() {}
