package external_test

import "testing"

// TestV lives in the external test package — v1 cannot build it.
func TestV(t *testing.T) {}
