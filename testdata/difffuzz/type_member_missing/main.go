package main

import (
	"encoding/json"
	"fmt"
)

func main() {
	fmt.Println("before")
	var x *json.Nope
	fmt.Println(x)
}
