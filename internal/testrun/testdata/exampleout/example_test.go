package exampleout

import (
	"fmt"
	"testing"
)

// ExampleP would actually run under go test — the // Output: comment
// makes skipping it a divergence, so the suite reports it and refuses
// to run at all.
func ExampleP() {
	fmt.Println("hello")
	// Output: hello
}

// TestOk is never reached: the output example above is fatal.
func TestOk(t *testing.T) {}
