package main

import "fmt"

func main() {
	msg := "before"
	fmt.Println(msg, "!!")
	{
		msg := "shadow"
		fmt.Println("**", msg, "**")
	}
	fmt.Println(msg, "!!")
	msg = "after"
	fmt.Println(msg, "!!")

	// Output:
	// before !!
	// ** shadow **
	// before !!
	// after !!
}
