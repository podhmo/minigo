package generator

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestImportManagerAdd pins the alias policy: sanitization, the
// keyword escape, conflict numbering, and the degenerate fallbacks.
func TestImportManagerAdd(t *testing.T) {
	tests := []struct {
		name     string
		current  string
		adds     [][2]string // (path, requestedAlias) pairs, in order
		want     []string
		wantImps map[string]string
	}{
		{
			name:     "empty path is not imported",
			adds:     [][2]string{{"", "x"}},
			want:     []string{""},
			wantImps: map[string]string{},
		},
		{
			name:     "current package qualifies unqualified",
			current:  "example.com/m/gen",
			adds:     [][2]string{{"example.com/m/gen", "gen"}},
			want:     []string{""},
			wantImps: map[string]string{},
		},
		{
			name: "base name and idempotent re-add",
			adds: [][2]string{
				{"example.com/m/foo", ""},
				{"example.com/m/foo", "ignored-second-alias"},
			},
			want:     []string{"foo", "foo"},
			wantImps: map[string]string{"example.com/m/foo": "foo"},
		},
		{
			name:     "requested alias wins over base",
			adds:     [][2]string{{"example.com/m/foo", "bar"}},
			want:     []string{"bar"},
			wantImps: map[string]string{"example.com/m/foo": "bar"},
		},
		{
			name:     "path separators are sanitized",
			adds:     [][2]string{{"example.com/m/my-pkg", ""}},
			want:     []string{"my_pkg"},
			wantImps: map[string]string{"example.com/m/my-pkg": "my_pkg"},
		},
		{
			name: "sanitized spellings collide and number",
			adds: [][2]string{
				{"a/my-pkg", ""},
				{"b/my.pkg", ""},
				{"c/mypkg", "my_pkg"},
			},
			want: []string{"my_pkg", "my_pkg1", "my_pkg2"},
			wantImps: map[string]string{
				"a/my-pkg": "my_pkg",
				"b/my.pkg": "my_pkg1",
				"c/mypkg":  "my_pkg2",
			},
		},
		{
			name:     "keyword base escapes with _pkg",
			adds:     [][2]string{{"example.com/m/type", ""}, {"example.com/m/range", ""}},
			want:     []string{"type_pkg", "range_pkg"},
			wantImps: map[string]string{"example.com/m/type": "type_pkg", "example.com/m/range": "range_pkg"},
		},
		{
			name:     "keyword alias conflicts also number",
			adds:     [][2]string{{"a/type", ""}, {"b/type", ""}},
			want:     []string{"type_pkg", "type_pkg1"},
			wantImps: map[string]string{"a/type": "type_pkg", "b/type": "type_pkg1"},
		},
		{
			name:     "non-identifier candidate gets pkg_ prefix",
			adds:     [][2]string{{"example.com/m/x", "123"}},
			want:     []string{"pkg_123"},
			wantImps: map[string]string{"example.com/m/x": "pkg_123"},
		},
		{
			name:     "blank identifier alias is refused",
			adds:     [][2]string{{"example.com/m/x", "."}},
			want:     []string{"pkg__"},
			wantImps: map[string]string{"example.com/m/x": "pkg__"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			im := NewImportManager(tt.current)
			got := make([]string, 0, len(tt.adds))
			for _, a := range tt.adds {
				got = append(got, im.Add(a[0], a[1]))
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Add() aliases mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantImps, im.Imports()); diff != "" {
				t.Errorf("Imports() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestImportManagerQualify pins the qualification contract: types in
// the current package or without a path stay bare, and lazily-added
// paths use the same alias every later call sees.
func TestImportManagerQualify(t *testing.T) {
	tests := []struct {
		name    string
		current string
		adds    [][2]string
		queries [][2]string // (packagePath, typeName) pairs
		want    []string
	}{
		{
			name:    "builtin and current-package types stay bare",
			current: "example.com/m/gen",
			queries: [][2]string{{"", "int"}, {"example.com/m/gen", "Local"}},
			want:    []string{"int", "Local"},
		},
		{
			name:    "foreign type qualifies by path base",
			queries: [][2]string{{"example.com/m/foo", "T"}, {"example.com/m/foo", "U"}},
			want:    []string{"foo.T", "foo.U"},
		},
		{
			name:    "explicit alias registered earlier is used",
			adds:    [][2]string{{"example.com/m/foo", "custom"}},
			queries: [][2]string{{"example.com/m/foo", "T"}},
			want:    []string{"custom.T"},
		},
		{
			name:    "basename conflicts number in qualification order",
			queries: [][2]string{{"a/x", "T"}, {"b/x", "T"}, {"a/x", "U"}},
			want:    []string{"x.T", "x1.T", "x.U"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			im := NewImportManager(tt.current)
			for _, a := range tt.adds {
				im.Add(a[0], a[1])
			}
			got := make([]string, 0, len(tt.queries))
			for _, q := range tt.queries {
				got = append(got, im.Qualify(q[0], q[1]))
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Qualify() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestImportManagerImportsCopy pins that Imports() hands out a
// snapshot — mutating it must not corrupt later registrations.
func TestImportManagerImportsCopy(t *testing.T) {
	im := NewImportManager("")
	im.Add("example.com/m/foo", "")
	snapshot := im.Imports()
	snapshot["example.com/m/foo"] = "corrupted"
	snapshot["unexpected"] = "entry"

	want := map[string]string{"example.com/m/foo": "foo"}
	if diff := cmp.Diff(want, im.Imports()); diff != "" {
		t.Errorf("Imports() after mutation mismatch (-want +got):\n%s", diff)
	}
}
