package main

import "fmt"

const raw = `{"a":1}`
const greets = "héllo"

type B []byte

func main() {
	b := []byte(raw)
	fmt.Println(len(b), string(b[:4]))
	r := []rune(greets)
	fmt.Println(len(r), string(r))
	var s = "xy"
	fmt.Println(len([]byte(s)), string(B(greets)[:2]))
}
