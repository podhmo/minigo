package main

// as plugin package, build with `go build -buildmode=plugin -o strings.so strings/`

import "strings"

func ToUpper(s string) string {
	return strings.ToUpper(s)
}
