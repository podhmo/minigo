package main

import "fmt"

// scratch-slot pressure: selects, type switches and ranges declare
// compiler-synthesized $-names; nesting them keeps sibling slots live
// while the inner construct mints its own.
func main() {
	c1, c2 := make(chan int, 1), make(chan int, 1)
	c1 <- 1
	c2 <- 2
	select {
	case x := <-c1:
		select {
		case y := <-c2:
			fmt.Println("nested-sel", x, y)
		}
	}
	select {
	case c1 <- 3:
	case z := <-c2:
		_ = z
	}
	select {
	case z := <-c1:
		fmt.Println("seq-sel", z)
	}
	var v any = 7
	switch v.(type) {
	case int:
		var w any = "s"
		switch w.(type) {
		case string:
			fmt.Println("nested-switch", v, w)
		}
	}
	for _, i := range []int{4, 5} {
		switch v.(type) {
		case int:
			fmt.Println("range-switch", i, v)
		}
	}
	f := func(int, int) int { return 9 }
	fmt.Println("funclit", f(1, 2))
}
