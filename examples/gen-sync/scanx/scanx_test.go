package scanx

import (
	"testing"
)

func TestFindSentinel(t *testing.T) {
	lines := []string{
		"package x",
		"",
		"// this file is not managed by gen-sync tooling",
		"/*",
		"// Code generated directives below are managed by gen-sync. DO NOT EDIT.",
		"*/",
		"const s = `",
		"// Code generated directives below are managed by gen-sync. DO NOT EDIT.",
		"`",
		Sentinel,
		"//go:generate stringer -type=X",
	}
	if got := FindSentinel(lines); got != 9 {
		t.Errorf("FindSentinel = %d; want 9 (prose, block comment, and raw string must not match)", got)
	}
	if got := FindSentinel(lines[:9]); got != -1 {
		t.Errorf("FindSentinel = %d; want -1 when the real sentinel is absent", got)
	}
}

func TestIsGenerateDirective(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"//go:generate stringer -type=X", true},
		{"  //go:generate stringer -type=X", true},
		{"//go:generate", true},
		{"//go:generate-not-directive", false},
		{"x := 1 //go:generate trailing", false},
		{"// other comment", false},
	}
	for _, c := range cases {
		if got := IsGenerateDirective(c.line); got != c.want {
			t.Errorf("IsGenerateDirective(%q) = %v; want %v", c.line, got, c.want)
		}
	}
}

func TestGenerateRunEnd(t *testing.T) {
	lines := []string{
		"//go:generate stringer -type=X",
		"",
		"//go:generate mockgen -source=x.go",
		"",
		"",
		"// user content",
		"//go:generate hand-written survives",
		"type X int",
	}
	if got := GenerateRunEnd(lines); got != 5 {
		t.Errorf("GenerateRunEnd = %d; want 5 (blank lines belong to the run, user content stops it)", got)
	}
}

func TestInsertAnchor(t *testing.T) {
	lines := []string{
		"// Package x docs.",
		"package x",
		"",
		"import (",
		"	\"fmt\"",
		")",
		"",
		"// a comment",
		"type X int",
	}
	if got := InsertAnchor(lines); got != 6 {
		t.Errorf("InsertAnchor = %d; want 6 (after the import block)", got)
	}

	// a one-line grouped import is complete on its own line — the
	// anchor must advance past it, not stay at the package clause.
	lines = []string{
		"package x",
		"",
		`import ("fmt")`,
		"",
		"type X int",
	}
	if got := InsertAnchor(lines); got != 3 {
		t.Errorf("InsertAnchor = %d; want 3 (after the single-line import)", got)
	}
}
