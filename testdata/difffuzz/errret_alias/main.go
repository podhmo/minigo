package main

import "fmt"

type Text = string
type E struct{}

func (E) Error() Text { return "error text" }

func main() { fmt.Println(E{}) }
