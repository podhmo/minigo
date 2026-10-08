// Package constinitvar feeds the const-init fallback: its const
// initializer references a package var, so only the full initializer
// can bind it.
package constinitvar

// Arr is a package var — its initializer records that init ran by
// bumping Side.
var Arr = mark()
var Side = 0

func mark() [4]int {
	Side++
	return [4]int{1, 2, 3, 4}
}

// N references the var Arr: len of an array operand is a constant
// expression in Go, but evaluating the operand needs the var bound —
// a const-only init cannot, so the full initializer runs.
const N = len(Arr)
