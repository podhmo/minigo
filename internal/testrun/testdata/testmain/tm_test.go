package testmain

import (
	"os"
	"testing"
)

// TestMain gates the whole suite under go test; minigo test v1 does not
// run it, so it reports the suite failed instead of diverging.
func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func TestV(t *testing.T) {}
