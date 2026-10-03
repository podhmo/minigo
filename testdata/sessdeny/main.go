package main

import "strings"

// Main touches a bound stdlib symbol so the import must resolve —
// a ModeDeny policy on "strings" turns the import itself into an error.
func Main() string {
	return strings.ToUpper("ok")
}

func main() {}
