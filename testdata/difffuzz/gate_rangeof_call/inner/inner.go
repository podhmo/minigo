package inner

func Ints() []int { return []int{1, 2} }

// Ch returns a buffered channel with one element already queued, so a
// receive from the call's result does not block.
func Ch() chan int {
	c := make(chan int, 1)
	c <- 9
	return c
}
