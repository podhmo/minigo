package internal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/podhmo/minigo/examples/convert-define/generator"
	"github.com/podhmo/minigo/examples/convert-define/model"
)

// writeNestedModule creates a temp module whose src/dst structs share a
// nested struct shape, exercising dotted field paths in c.Map calls.
// It returns the directory; define files are written per test.
func writeNestedModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/nested\n\ngo 1.22\n",
		"source/source.go": `package source

type Src struct {
	ID    int64
	Inner Inner
	PIn   *Inner
	Name  string
}

type Inner struct {
	ID    int64
	Value string
}
`,
		"destination/destination.go": `package destination

type Dst struct {
	Inner Inner
	PIn   *Inner
	Flat  string
	Tag   string
}

type Inner struct {
	ID    int64
	Value string
}
`,
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile(%q): %v", path, err)
		}
	}
	return dir
}

func writeDefine(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "define.go")
	content := `//go:build codegen

package main

import (
	"example.com/nested/destination"
	"example.com/nested/source"
	"github.com/podhmo/minigo/examples/convert-define/define"
)

func main() {
` + body + `
}
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
	return path
}

// TestParserNestedPaths: c.Map accepts dotted paths on both sides —
// dst.Inner.ID, src.Inner.Value, src.PIn.Value (pointer intermediate)
// and dst.PIn.Value (pointer intermediate needing nil-init) — and the
// generated code carries the right guards.
func TestParserNestedPaths(t *testing.T) {
	dir := writeNestedModule(t)
	defineFile := writeDefine(t, dir, `	define.Convert(func(c *define.Config, dst *destination.Dst, src *source.Src) {
		c.Map(dst.Inner.ID, src.ID)
		c.Map(dst.Flat, src.Inner.Value)
		c.Map(dst.Tag, src.PIn.Value)
		c.Map(dst.PIn.Value, src.Name)
	})`)

	runner, err := NewRunner()
	if err != nil {
		t.Fatalf("NewRunner() failed: %+v", err)
	}
	if err := runner.Run(context.Background(), defineFile); err != nil {
		t.Fatalf("Run() failed: %+v", err)
	}

	if want, got := 1, len(runner.Info.ConversionPairs); want != got {
		t.Fatalf("expected %d conversion pair, got %d", want, got)
	}
	pair := runner.Info.ConversionPairs[0]
	if pair.Mapping == nil {
		t.Fatal("pair.Mapping is nil")
	}
	wantMaps := []model.FieldMap{
		{SrcName: "ID", DstName: "Inner.ID"},
		{SrcName: "Inner.Value", DstName: "Flat"},
		{SrcName: "PIn.Value", DstName: "Tag"},
		{SrcName: "Name", DstName: "PIn.Value"},
	}
	if diff := cmp.Diff(wantMaps, pair.Mapping.Maps); diff != "" {
		t.Errorf("pair.Mapping.Maps mismatch (-want +got):\n%s", diff)
	}

	out, err := generator.Generate(runner.TypeResolver(), runner.Info, generator.Options{})
	if err != nil {
		t.Fatalf("Generate() failed: %+v", err)
	}
	code := string(out)
	for _, want := range []string{
		// struct auto-conversion runs first; the explicit leaf write
		// overrides the ancestor's copied leaf afterwards.
		"dst.Inner = *convertInnerToInner(ctx, ec, &src.Inner)",
		"dst.Inner.ID = src.ID",
		"dst.Flat = src.Inner.Value",
		// pointer src intermediate is nil-guarded
		"if src.PIn != nil {",
		"dst.Tag = src.PIn.Value",
		// pointer dst intermediate is nil-initialised before the write
		"if dst.PIn == nil {",
		"dst.PIn.Value = src.Name",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("generated code missing %q\n---\n%s", want, code)
		}
	}
}

// TestParserNestedPathErrors: bad path segments report at the DSL call
// site instead of generating broken code.
func TestParserNestedPathErrors(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name: "missing dst intermediate",
			body: `	define.Convert(func(c *define.Config, dst *destination.Dst, src *source.Src) {
		c.Map(dst.Nope.ID, src.ID)
	})`,
			wantErr: `has no field "Nope"`,
		},
		{
			name: "missing src intermediate",
			body: `	define.Convert(func(c *define.Config, dst *destination.Dst, src *source.Src) {
		c.Map(dst.Flat, src.Nope.Value)
	})`,
			wantErr: `has no field "Nope"`,
		},
		{
			name: "leaf intermediate is not selectable",
			body: `	define.Convert(func(c *define.Config, dst *destination.Dst, src *source.Src) {
		c.Map(dst.Flat.X, src.Name)
	})`,
			wantErr: "not a selectable struct",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeNestedModule(t)
			defineFile := writeDefine(t, dir, tc.body)

			runner, err := NewRunner()
			if err != nil {
				t.Fatalf("NewRunner() failed: %+v", err)
			}
			err = runner.Run(context.Background(), defineFile)
			if err == nil {
				t.Fatal("Run() succeeded, expected a field-path error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Run() error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestParserSelfPackageTypes: unqualified type names in a define.Convert
// signature resolve through the define file's directory package — the
// file-loaded package's synthetic "<file>..." path holds no sibling decls.
func TestParserSelfPackageTypes(t *testing.T) {
	dir := writeNestedModule(t)
	if err := os.WriteFile(filepath.Join(dir, "types.go"), []byte(`package main

type SrcSelf struct {
	V int64
}

type DstSelf struct {
	V int64
}
`), 0644); err != nil {
		t.Fatalf("WriteFile(types.go): %v", err)
	}
	defineFile := writeDefine(t, dir, `	define.Convert(func(c *define.Config, dst *DstSelf, src *SrcSelf) {
	})`)

	runner, err := NewRunner()
	if err != nil {
		t.Fatalf("NewRunner() failed: %+v", err)
	}
	if err := runner.Run(context.Background(), defineFile); err != nil {
		t.Fatalf("Run() failed: %+v", err)
	}

	if want, got := 1, len(runner.Info.ConversionPairs); want != got {
		t.Fatalf("expected %d conversion pair, got %d", want, got)
	}
	pair := runner.Info.ConversionPairs[0]
	if want, got := "SrcSelf", pair.SrcTypeName; want != got {
		t.Errorf("SrcTypeName: want %q, got %q", want, got)
	}
	if want, got := "DstSelf", pair.DstTypeName; want != got {
		t.Errorf("DstTypeName: want %q, got %q", want, got)
	}

	out, err := generator.Generate(runner.TypeResolver(), runner.Info, generator.Options{})
	if err != nil {
		t.Fatalf("Generate() failed: %+v", err)
	}
	if want := "dst.V = src.V"; !strings.Contains(string(out), want) {
		t.Errorf("generated code missing %q\n---\n%s", want, out)
	}
}

// TestParserConvertCallRejectsNonFunction: a non-function converter
// (a call expression like define.Rule(fn), or any other expression)
// reports at the DSL call site instead of slipping into generated code.
func TestParserConvertCallRejectsNonFunction(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name: "define.Rule call is not a function reference",
			body: `	define.Convert(func(c *define.Config, dst *destination.Dst, src *source.Src) {
		c.Convert(dst.Flat, src.Name, define.Rule(source.Src))
	})`,
			wantErr: "must be a function",
		},
		{
			name: "factory call is not a function reference",
			body: `	define.Convert(func(c *define.Config, dst *destination.Dst, src *source.Src) {
		c.Convert(dst.Flat, src.Name, source.Maker())
	})`,
			wantErr: "must be a function",
		},
		{
			name: "func literal without the (ctx, ec, src) params",
			body: `	define.Convert(func(c *define.Config, dst *destination.Dst, src *source.Src) {
		c.Convert(dst.Flat, src.Name, func(s string) string { return s + "!" })
	})`,
			wantErr: "got 1 param(s) and 1 result(s)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeNestedModule(t)
			defineFile := writeDefine(t, dir, tc.body)

			runner, err := NewRunner()
			if err != nil {
				t.Fatalf("NewRunner() failed: %+v", err)
			}
			err = runner.Run(context.Background(), defineFile)
			if err == nil {
				t.Fatal("Run() succeeded, expected a converter-shape error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Run() error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestParserConvertCallFuncLit: a func literal converter is a function —
// it flows through to the generated call site verbatim.
func TestParserConvertCallFuncLit(t *testing.T) {
	dir := writeNestedModule(t)
	defineFile := writeDefine(t, dir, `	define.Convert(func(c *define.Config, dst *destination.Dst, src *source.Src) {
		c.Convert(dst.Flat, src.Name, func(ctx context.Context, ec *model.ErrorCollector, s string) string { return s + "!" })
	})`)

	runner, err := NewRunner()
	if err != nil {
		t.Fatalf("NewRunner() failed: %+v", err)
	}
	if err := runner.Run(context.Background(), defineFile); err != nil {
		t.Fatalf("Run() failed: %+v", err)
	}
	if want, got := 1, len(runner.Info.ConversionPairs[0].Mapping.Maps); want != got {
		t.Fatalf("expected %d field map, got %d", want, got)
	}
	if want := `func(ctx context.Context, ec *model.ErrorCollector, s string) string { return s + "!" }`; runner.Info.ConversionPairs[0].Mapping.Maps[0].Converter != want {
		t.Errorf("Converter: want %q, got %q", want, runner.Info.ConversionPairs[0].Mapping.Maps[0].Converter)
	}
}

// TestParserExplicitTypeArgs: explicit generic instantiation on the DSL
// calls — define.Convert[Dst, Src](...), c.Convert[D, S](...),
// c.Compute[T](...) — is legal under the generic define API, and the
// interpreter accepts it leniently by unwrapping the type args before
// dispatching the special form / matching the mapping method name.
func TestParserExplicitTypeArgs(t *testing.T) {
	dir := writeNestedModule(t)
	defineFile := writeDefine(t, dir, `	define.Convert[destination.Dst, source.Src](func(c *define.Config, dst *destination.Dst, src *source.Src) {
		c.Convert[string, string](dst.Flat, src.Name, func(ctx context.Context, ec *model.ErrorCollector, s string) string { return s + "!" })
		c.Compute[string](dst.Tag, src.Name)
		c.Map[int64](dst.Inner.ID, src.ID) // lenient: Map is not generic, instantiation still accepted
	})`)

	runner, err := NewRunner()
	if err != nil {
		t.Fatalf("NewRunner() failed: %+v", err)
	}
	if err := runner.Run(context.Background(), defineFile); err != nil {
		t.Fatalf("Run() failed: %+v", err)
	}

	if want, got := 1, len(runner.Info.ConversionPairs); want != got {
		t.Fatalf("expected %d conversion pair, got %d", want, got)
	}
	pair := runner.Info.ConversionPairs[0]
	wantMaps := []model.FieldMap{
		{SrcName: "Name", DstName: "Flat", Converter: `func(ctx context.Context, ec *model.ErrorCollector, s string) string { return s + "!" }`},
		{SrcName: "ID", DstName: "Inner.ID"},
	}
	if diff := cmp.Diff(wantMaps, pair.Mapping.Maps); diff != "" {
		t.Errorf("pair.Mapping.Maps mismatch (-want +got):\n%s", diff)
	}
	wantComputed := []model.ComputedField{
		{DstName: "Tag", Expr: "src.Name"},
	}
	if diff := cmp.Diff(wantComputed, pair.Computed, cmpopts.IgnoreFields(model.ComputedField{}, "ExprType")); diff != "" {
		t.Errorf("pair.Computed mismatch (-want +got):\n%s", diff)
	}
	if te := pair.Computed[0].ExprType; te == nil || te.Text != "string" {
		t.Errorf("pair.Computed[0].ExprType: want string, got %v", te)
	}
}
