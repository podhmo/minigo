package main

func swap(a, b int) (int, int) { return b, a }

func main() {
	println(swap(swap(1, 2)))
}
