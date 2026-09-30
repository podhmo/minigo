package goscan

import (
	"context"
	"testing"
)

func TestScanStdlib_SucceedsWithoutOverride(t *testing.T) {
	// This test verifies that the scanner can successfully scan a stdlib package
	// like "time" from within a test binary, without needing an ExternalTypeOverride.
	// This is made possible by the two-pass scanning logic in `scanGoFiles` which
	// correctly identifies the dominant package name and ignores the "main" package
	// injected by the `go test` environment.
	ctx := context.Background()

	s, err := New(WithGoModuleResolver())
	if err != nil {
		t.Fatalf("New() failed: %+v", err)
	}

	pkg, err := s.ScanPackageFromImportPath(ctx, "time")
	if err != nil {
		t.Fatalf("expected no error when scanning stdlib package 'time', but got: %v", err)
	}

	if pkg == nil {
		t.Fatal("scanner returned a nil package")
	}
	if pkg.Name != "time" {
		t.Errorf("expected package name to be 'time', but got %q", pkg.Name)
	}

	// Check if a well-known type from the time package was found.
	timeType := pkg.Lookup("Time")
	if timeType == nil {
		t.Fatal("could not find type 'Time' in scanned 'time' package")
	}
	if timeType.Kind != StructKind {
		t.Errorf("expected 'time.Time' to be a struct, but was %v", timeType.Kind)
	}
}
