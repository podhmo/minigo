package main

import "fmt"

// P4-4a: OpCall's static-typedef tier moved to the call site's ArgHint
// record — generic inference resolves the recipes lazily, and normal,
// variadic and lone-call-spread calls must infer identically.

func id[T any](v T) T          { return v }
func pair[T any, U any](t T, u U) (T, U) { return t, u }
func collect[T any](xs ...T) int       { return len(xs) }
func conv[T any](v T) T        { return v }

var gint = 40

func main() {
	// local-slot hint + global hint + conversion-target hint.
	loc := int64(7)
	fv := 3.9
	fmt.Println(id(loc), id(gint), conv(int(fv)))

	// variadic: spread and non-spread infer the same T.
	xs := []string{"a", "bb"}
	fmt.Println(collect(xs...), collect("x", "yy"))

	// lone call spread: swap's tuple expands into pair's params.
	a, b := pair(pair(1, "s"))
	fmt.Println(a, b)

	// upval hint: the closure's arg infers from the captured var's type.
	up := 2.5
	f := func() float64 { return id(up) }
	fmt.Println(f())
}
