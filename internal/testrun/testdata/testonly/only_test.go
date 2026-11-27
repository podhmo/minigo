package testonly

import "testing"

// A test-only directory: no non-test .go files exist, yet go test
// still builds and runs the test files alone.
func TestAlone(t *testing.T) {
	t.Log("only test files here")
}
