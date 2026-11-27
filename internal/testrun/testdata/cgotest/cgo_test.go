package cgotest

import "C"
import "testing"

// TestCgo never runs: the file imports "C", which cgo-less minigo
// drops from the build — reported loudly so the test is not silently
// absent.
func TestCgo(t *testing.T) {}
