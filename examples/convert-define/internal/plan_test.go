package internal

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	goscan "github.com/podhmo/go-scan"
	"github.com/podhmo/minigo/resolve"
)

// spyResolver records every package the minigo engine resolves. The plan's
// core laziness claim (sketch/plan-minigo-vm.md §12.2) is that quoted
// special calls never materialize what they reference — so on a fully
// lazy run these lists stay empty.
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
//   - quoting never materializes: the engine's resolver sees zero Locate /
//     LocateDir calls — define, convutil, source and destination are all
//     only read host-side or left as AST;
//   - the //go:build codegen DSL file is a first-class entry (LoadFile).
func TestConvertDefineSatisfiesPlan(t *testing.T) {
	wd := filepath.Join("..", "testdata", "plan")

	runner, err := NewRunner(
		goscan.WithWorkDir(wd),
		goscan.WithGoModuleResolver(),
	)
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
	srcInfo := findField(t, runner.Info.Structs["SrcUser"], "ID")
	if want, got := "UserID", srcInfo.Tag.DstFieldName; want != got {
		t.Errorf("ID tag DstFieldName: want %q, got %q", want, got)
	}

	// The laziness claim: quoting an import never locates the package.
	// define (special), bogus (dead branch), convutil/source/destination
	// (quoted args read host-side) never hit the engine's resolver.
	if len(spy.located) != 0 {
		t.Errorf("engine materialized packages: %v", spy.located)
	}
	if len(spy.dirs) != 0 {
		t.Errorf("engine located directories: %v", spy.dirs)
	}
}
