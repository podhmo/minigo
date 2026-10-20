package main

// A param is in scope only over the body, so the result type any is
// the go1.18 predeclared name.
func F(any int) any { return any }

func main() {}
