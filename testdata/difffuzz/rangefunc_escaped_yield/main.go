package main

import "fmt"

var saved func(int) bool

func seq(yield func(int) bool) {
	saved = yield
	for i := 0; i < 2; i++ {
		if !yield(i) {
			return
		}
	}
}

func loop() {
	for x := range seq {
		fmt.Println("body", x)
	}
}

func loopBreak() {
	for x := range seq {
		fmt.Println("break body", x)
		break
	}
}

func call(label string) (r int) {
	defer func() { fmt.Println(label, "recovered:", recover()) }()
	a := 1
	saved(10)
	fmt.Println(label, "after saved", a)
	return a
}

func nested() {
	for x := range seq {
		for y := range seq {
			fmt.Println("nested", x, y)
		}
	}
	call("nested")
}

func main() {
	loop()
	fmt.Println(call("whole"))
	loopBreak()
	fmt.Println(call("break"))
	nested()
	loop()
	fmt.Println(call("again"))
}
