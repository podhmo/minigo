package main

import (
	"os"
	"strings"
)

// PolicyOK uses an allowed symbol (strings.ToUpper).
func PolicyOK() string { return strings.ToUpper("ok") }

// PolicyDenied calls os.Getenv — removed by the test's WithHostPolicy.
func PolicyDenied() string { return os.Getenv("HOME") }

func main() {}
