package main

import "fmt"

func main() {
	msg := "before"
	fmt.Println(msg, "!!")
	{
		msg := "shaddow"
		fmt.Println(msg, "**")
	}
	fmt.Println(msg, "!!")
	msg = "after"
	fmt.Println(msg, "!!")
}
