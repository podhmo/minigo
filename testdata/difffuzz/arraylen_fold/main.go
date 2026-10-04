package main

import "fmt"

const N = 3

func main() {
	// `[...]` takes the literal's count.
	at := [...]*int{new(int), new(int), new(int)}
	fmt.Println(len(at))

	// `[len(x)]T` folds to the concrete length, nested or not.
	aat := [][len(at)]*int{at, at}
	fmt.Println(len(aat), len(aat[1]))

	// named consts fold the same way.
	var bn [N]int
	nb := [][N]int{bn}
	fmt.Println(len(nb[0]))

	// mixed arithmetic still folds.
	var w [2 * N]int
	fmt.Println(len(w))
}
