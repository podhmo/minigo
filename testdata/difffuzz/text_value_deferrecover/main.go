package main

// recover1.go's sequential-panic shapes: a `defer recover()` inside a
// deferred call is a no-op while its own frame unwinds (test7), but at
// the frame's epilogue it catches the panic unwinding below (test6).
func mustRec(x int) {
	if r := recover(); r != x {
		println("mustRec got", r, "want", x)
	}
}
func mustNot() {
	if r := recover(); r != nil {
		println("spurious", r)
	}
}
func test6() {
	defer mustNot()
	defer func() {
		defer recover()
		defer mustRec(3)
		panic(3)
	}()
	panic(2)
}
func test7() {
	defer mustRec(2)
	defer func() {
		defer mustRec(3)
		defer recover()
		panic(3)
	}()
	panic(2)
}
func main() {
	test6()
	test7()
	println("done")
}
