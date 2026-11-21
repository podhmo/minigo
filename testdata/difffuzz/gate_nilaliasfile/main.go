package main

import "fmt"

// main.go does not import inner at all — resolving S's declared type
// against this file's imports would fail.
func main() {
	fmt.Println(S == nil)
}
