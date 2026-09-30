package scanner

import (
	"context"
	"testing"
)

func TestNewUnresolvedTypeInfo(t *testing.T) {
	pkgPath := "example.com/foo"
	name := "Bar"

	ti := NewUnresolvedTypeInfo(pkgPath, name)

	if ti == nil {
		t.Fatal("NewUnresolvedTypeInfo returned nil")
	}
	if ti.PkgPath != pkgPath {
		t.Errorf("got PkgPath %q, want %q", ti.PkgPath, pkgPath)
	}
	if ti.Name != name {
		t.Errorf("got Name %q, want %q", ti.Name, name)
	}
	if !ti.Unresolved {
		t.Error("got Unresolved = false, want true")
	}
}

func TestTypeInfo_Annotation(t *testing.T) {
	tests := []struct {
		name      string
		doc       string
		annoName  string
		wantValue string
		wantOk    bool
	}{
		{
			name:      "basic case with colon",
			doc:       "// @foo: bar",
			annoName:  "foo",
			wantValue: "bar",
			wantOk:    true,
		},
		{
			name:      "basic case with space",
			doc:       "// @foo bar",
			annoName:  "foo",
			wantValue: "bar",
			wantOk:    true,
		},
		{
			name:      "no value",
			doc:       "// @foo",
			annoName:  "foo",
			wantValue: "",
			wantOk:    true,
		},
		{
			name:      "leading and trailing spaces on line",
			doc:       "   // @foo: bar   ",
			annoName:  "foo",
			wantValue: "bar",
			wantOk:    true,
		},
		{
			name:      "space around separator",
			doc:       "// @foo : bar",
			annoName:  "foo",
			wantValue: "bar",
			wantOk:    true,
		},
		{
			name:      "value with spaces",
			doc:       "// @foo: bar baz qux",
			annoName:  "foo",
			wantValue: "bar baz qux",
			wantOk:    true,
		},
		{
			name:      "multi-line doc comment",
			doc:       "// This is a struct.\n// @foo: bar\n// More comments.",
			annoName:  "foo",
			wantValue: "bar",
			wantOk:    true,
		},
		{
			name:      "annotation not present",
			doc:       "// This is a struct.",
			annoName:  "foo",
			wantValue: "",
			wantOk:    false,
		},
		{
			name:      "multiple annotations, find first",
			doc:       "// @foo: bar\n// @bar: baz",
			annoName:  "foo",
			wantValue: "bar",
			wantOk:    true,
		},
		{
			name:      "complex name with value",
			doc:       `// @deriving:binding in:"body" required`,
			annoName:  "deriving:binding",
			wantValue: `in:"body" required`,
			wantOk:    true,
		},
		{
			name:      "complex name with colon separator",
			doc:       `// @deriving:binding: in:"body" required`,
			annoName:  "deriving:binding",
			wantValue: `in:"body" required`,
			wantOk:    true,
		},
		{
			name:      "empty doc",
			doc:       "",
			annoName:  "foo",
			wantValue: "",
			wantOk:    false,
		},
		{
			name:      "annotation is the whole line",
			doc:       "@foo:bar",
			annoName:  "foo",
			wantValue: "bar",
			wantOk:    true,
		},
		{
			name:      "annotation with only spaces after colon",
			doc:       "@foo:   ",
			annoName:  "foo",
			wantValue: "",
			wantOk:    true,
		},
		{
			name:      "annotation name is a prefix of another",
			doc:       "@foobar: baz",
			annoName:  "foo",
			wantValue: "",
			wantOk:    false,
		},
		{
			name:      "annotation name followed by non-separator",
			doc:       "@foobar",
			annoName:  "foo",
			wantValue: "",
			wantOk:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ti := &TypeInfo{Doc: tt.doc}
			gotValue, gotOk := ti.Annotation(context.Background(), tt.annoName)
			if gotOk != tt.wantOk {
				t.Errorf("TypeInfo.Annotation() gotOk = %v, want %v", gotOk, tt.wantOk)
			}
			if gotValue != tt.wantValue {
				t.Errorf("TypeInfo.Annotation() gotValue = %q, want %q", gotValue, tt.wantValue)
			}
		})
	}
}
