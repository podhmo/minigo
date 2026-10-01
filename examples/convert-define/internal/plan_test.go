package internal

import (
	"context"
	"path/filepath"
	"testing"

	"slices"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/examples/convert-define/model"
	"github.com/podhmo/minigo/resolve"
)

// spyResolver records every package the minigo engine resolves. The plan's
// core laziness claim (docs/sketch/plan-minigo-vm.md §12.2) is that quoted
// special calls never materialize what they reference at eval time — the
// interpreter itself locates nothing, and the handlers then pay only for
// the packages the DSL actually names.
type spyResolver struct {
	inner   resolve.Resolver
	located []string
	dirs    []string
}

func (s *spyResolver) Locate(ctx context.Context, fromDir, importPath string) (*resolve.PackageMeta, error) {
	s.located = append(s.located, importPath)
	return s.inner.Locate(ctx, fromDir, importPath)
}

func (s *spyResolver) LocateDir(ctx context.Context, dir string) (*resolve.PackageMeta, error) {
	s.dirs = append(s.dirs, dir)
	return s.inner.LocateDir(ctx, dir)
}

// TestConvertDefineSatisfiesPlan runs testdata/plan/define.go through the
// real Runner and asserts the migration satisfies plan-minigo-vm.md §12:
//
//   - dispatch is by canonical symbol identity: the `d`-aliased define
//     import still compiles to SPECIAL_CALL;
//   - specials fire only when reached: the dead `if false` branch never
//     evaluates `bogus.Nope` (a package that does not exist);
//   - quoting never materializes at eval time: the engine's resolver sees
//     no Locate call from interpretation itself. Scanning happens inside
//     the special handlers through the same lazy loader (engine.Package),
//     so exactly the packages the DSL names are located — convutil,
//     source and destination — while define (a special, not a package)
//     and bogus (a dead branch) are never located;
//   - the //go:build codegen DSL file is a first-class entry (LoadFile).
func TestConvertDefineSatisfiesPlan(t *testing.T) {
	wd := filepath.Join("..", "testdata", "plan")

	runner, err := NewRunner()
	if err != nil {
		t.Fatalf("NewRunner() failed: %+v", err)
	}

	abs, err := filepath.Abs(wd)
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	inner, err := resolve.NewGoScanResolver(abs, resolve.BuildConfig{})
	if err != nil {
		t.Fatalf("NewGoScanResolver: %v", err)
	}
	spy := &spyResolver{inner: inner}
	runner.resolver = spy

	defineFile := filepath.Join(wd, "define.go")
	if err := runner.Run(context.Background(), defineFile); err != nil {
		t.Fatalf("Run() failed: %+v", err)
	}

	// The DSL file loads as a single-file package named main.
	if got := runner.PackageName(); got != "main" {
		t.Errorf("PackageName: got %q, want main", got)
	}
	if runner.pkg == nil || len(runner.pkg.Files) != 1 {
		t.Fatalf("file package must hold exactly the named file, got %+v", runner.pkg)
	}

	// The aliased `d.Rule` dispatched as SPECIAL_CALL and the handler
	// registered exactly one global rule; the dead `bogus.Nope` branch did
	// not fire a second one.
	if want, got := 1, len(runner.Info.GlobalRules); want != got {
		t.Fatalf("expected %d global rule (dead branch must not fire), got %d", want, got)
	}
	if want, got := "convutil.TimeToString", runner.Info.GlobalRules[0].UsingFunc; want != got {
		t.Errorf("GlobalRules[0].UsingFunc: want %q, got %q", want, got)
	}
	wantImports := map[string]string{"convutil": "example.com/plan/convutil"}
	if diff := cmp.Diff(wantImports, runner.Info.Imports); diff != "" {
		t.Errorf("Imports mismatch (-want +got):\n%s", diff)
	}

	// The aliased `d.Convert` likewise dispatched, and its quoted FuncLit
	// was walked as AST: src/dst resolved host-side, c.Map became a tag.
	if want, got := 1, len(runner.Info.ConversionPairs); want != got {
		t.Fatalf("expected %d conversion pair, got %d", want, got)
	}
	pair := runner.Info.ConversionPairs[0]
	if want, got := "SrcUser", pair.SrcTypeName; want != got {
		t.Errorf("pair.SrcTypeName: want %q, got %q", want, got)
	}
	if want, got := "DstUser", pair.DstTypeName; want != got {
		t.Errorf("pair.DstTypeName: want %q, got %q", want, got)
	}
	srcInfo := findField(t, runner.Info.Structs[model.DeclKey(pair.SrcTypeInfo)], "ID")
	if want, got := "UserID", srcInfo.Tag.DstFieldName; want != got {
		t.Errorf("ID tag DstFieldName: want %q, got %q", want, got)
	}

	// The laziness claim, restated for the inspect-based pipeline:
	// interpretation itself locates nothing — LoadFile reads only the
	// DSL file's own directory — while the special handlers resolve each
	// quoted argument's package on demand. Exactly the touched packages
	// are located; the `define` special and the dead `bogus` import are
	// never located.
	wantLocated := []string{
		"example.com/plan/convutil",
		"example.com/plan/destination",
		"example.com/plan/source",
	}
	slices.Sort(spy.located)
	if diff := cmp.Diff(wantLocated, spy.located); diff != "" {
		t.Errorf("located packages mismatch (-want +got):\n%s", diff)
	}
	// LoadFile reads the DSL file directly; the only directory located is
	// the DSL file's own, resolved once to learn the generated file's
	// package path.
	if diff := cmp.Diff([]string{abs}, spy.dirs); diff != "" {
		t.Errorf("located directories mismatch (-want +got):\n%s", diff)
	}
}
