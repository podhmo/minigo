package main

import "fmt"

// Counter's String is on the pointer receiver: a Counter value does not
// implement fmt.Stringer, so fmt prints the struct, not "#1".
type Counter struct{ N int }

func (c *Counter) String() string { return fmt.Sprint("#", c.N) }

func main() {
	fmt.Println(Counter{1}, &Counter{1})
}
