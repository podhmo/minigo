package main

func f() int        { return 10 }
func g() int        { return 20 }
func bar(x int) int { return x + 1 }
func foo(x, y int)  { println(x, y) }

func main() {
	foo(f(), bar(g()))
	foo(bar(f()), g())
}
