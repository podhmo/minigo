package scanner

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestScanFiles_InfiniteAliasRecursion verifies that ScanFiles does not
// enter an infinite loop when parsing mutually recursive type aliases.
func TestScanFiles_InfiniteAliasRecursion(t *testing.T) {
	// This test sets up a scenario with two types that are aliases for each other:
	// type T1 T2
	// type T2 T1
	// The scanner should be able to handle this without getting stuck in a loop
	// during the initial parsing phase within TypeInfoFromExpr.

	testDir := filepath.Join("..", "testdata", "recursion", "mutual_alias")
	absTestDir, err := filepath.Abs(testDir)
	if err != nil {
		t.Fatalf("could not get absolute path: %v", err)
	}
	s := newTestScanner(t, "example.com/test/recursion/mutual_alias", absTestDir)

	// Create a context with a timeout to prevent the test from hanging indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The actual test: Call ScanFiles on the file with the problematic code.
	// This is the call that should trigger the infinite recursion inside TypeInfoFromExpr.
	_, err = s.ScanFiles(ctx, []string{filepath.Join(testDir, "mutual.go")}, testDir)

	// Check if the context timed out.
	if err := ctx.Err(); err == context.DeadlineExceeded {
		t.Fatal("Test timed out: ScanFiles hung, indicating infinite recursion.")
	}

	// If the function returned (even with an error), the recursion was prevented.
	if err != nil {
		t.Logf("ScanFiles returned an error as expected (or unexpectedly): %v", err)
	}

	// The main success condition is not timing out.
}

// TestScanFiles_InfiniteGenericRecursion verifies that ScanFiles does not
// enter an infinite loop when parsing recursive generic type aliases.
func TestScanFiles_InfiniteGenericRecursion(t *testing.T) {
	// This test uses a recursive generic type: `type T G[T]`.
	// The `TypeInfoFromExpr` function would recursively call itself to resolve `T`
	// when parsing the type argument of `G`, leading to an infinite loop.

	testDir := filepath.Join("..", "testdata", "recursion", "generic_alias")
	absTestDir, err := filepath.Abs(testDir)
	if err != nil {
		t.Fatalf("could not get absolute path: %v", err)
	}
	s := newTestScanner(t, "example.com/test/recursion/generic_alias", absTestDir)

	// Create a context with a timeout to prevent the test from hanging indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The actual test: Call ScanFiles on the file with the problematic code.
	// This is the call that should trigger the infinite recursion inside TypeInfoFromExpr.
	_, err = s.ScanFiles(ctx, []string{filepath.Join(testDir, "generic.go")}, testDir)

	// Check if the context timed out.
	if err := ctx.Err(); err == context.DeadlineExceeded {
		t.Fatal("Test timed out: ScanFiles hung, indicating infinite recursion.")
	}

	// If the function returned (even with an error), the recursion was prevented.
	if err != nil {
		t.Logf("ScanFiles returned an error as expected (or unexpectedly): %v", err)
	}

	// The main success condition is not timing out.
}
