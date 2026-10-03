package scanx

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseTag(t *testing.T) {
	tag := `required:"true" json:"name,omitempty" validate:"min=1,required"`
	want := []TagField{
		{Key: "required", Value: "true"},
		{Key: "json", Value: "name,omitempty"},
		{Key: "validate", Value: "min=1,required"},
	}
	if diff := cmp.Diff(want, ParseTag(tag)); diff != "" {
		t.Errorf("ParseTag mismatch (-want +got):\n%s", diff)
	}
}

func TestLookupTag(t *testing.T) {
	tag := `json:"name" required:"true"`
	if v, ok := LookupTag(tag, "required"); !ok || v != "true" {
		t.Errorf("LookupTag(required) = %q, %v; want true, true", v, ok)
	}
	if _, ok := LookupTag(tag, "xml"); ok {
		t.Error("LookupTag(xml) = ok; want miss")
	}
	// a key that merely contains another's name is not a match
	if _, ok := LookupTag(`notrequired:"true"`, "required"); ok {
		t.Error("LookupTag found required inside notrequired")
	}
}

func TestTagHasElement(t *testing.T) {
	cases := []struct {
		tag  string
		want bool
	}{
		{`validate:"required"`, true},
		{`validate:"min=1,required"`, true},
		{`validate:"required,min=1"`, true},
		{`validate:"notrequired"`, false},
		{`binding:"required"`, false}, // different key
		{`json:"required,omitempty"`, false},
	}
	for _, c := range cases {
		if got := TagHasElement(c.tag, "validate", "required"); got != c.want {
			t.Errorf("TagHasElement(%q, validate, required) = %v; want %v", c.tag, got, c.want)
		}
	}
}

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

func TestPosFilePosLine(t *testing.T) {
	if got := PosFile("/a/b/c.go:12:5"); got != "/a/b/c.go" {
		t.Errorf("PosFile = %q", got)
	}
	if got := PosLine("/a/b/c.go:12:5"); got != 12 {
		t.Errorf("PosLine = %d", got)
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
}
