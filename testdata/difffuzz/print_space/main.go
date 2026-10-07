package main

// Builtin print joins operands with no separator at all (gc's behavior,
// unlike fmt.Fprint which spaces non-strings); println always spaces.

func f() (int16, float64, string) { return -42, 42.0, "x" }

func main() {
	print(1, 2)
	print("a", "b")
	print(1, "a", 2)
	print(1.5, 3)
	print(f())
	println(1, "a", 2)
	println(f())
}
