package main

import (
	"fmt"
	"sync"
	"time"
)

type Small int8
type Wide uint16
type Name string

// eq compares one shared constant site against operands of several
// types: each execution must convert the constant to the operand's own
// type, whatever the previous execution converted it to.
func eq(x any) string {
	switch v := x.(type) {
	case Small:
		return fmt.Sprintf("%T %v %v", v+1, v == 100, v+1)
	case Wide:
		return fmt.Sprintf("%T %v %v", v+1, v == 100, v+1)
	case rune:
		return fmt.Sprintf("%T %v %v", v+1, v == 100, v+1)
	case int:
		return fmt.Sprintf("%T %v %v", v+1, v == 100, v+1)
	case float64:
		return fmt.Sprintf("%T %v %v", v+1, v == 100, v+1)
	case time.Duration:
		return fmt.Sprintf("%T %v %v", v+1, v == 100, v+1)
	}
	return "?"
}

func label(n Name) bool { return n == "x" }

func main() {
	vals := []any{Small(100), Wide(100), rune(100), 100, 100.0, time.Duration(100), Small(7), Wide(65535)}
	for range 2 {
		for _, v := range vals {
			fmt.Println(eq(v))
		}
	}
	fmt.Println(label("x"), label("y"))

	var wg sync.WaitGroup
	res := make([]string, 8)
	for i := range res {
		wg.Go(func() {
			s := ""
			for range 200 {
				s = eq(vals[i%len(vals)])
			}
			res[i] = s
		})
	}
	wg.Wait()
	for _, s := range res {
		fmt.Println(s)
	}
}
