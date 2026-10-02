package httpinspect_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/experiments/httpinspect"
	"github.com/podhmo/minigo/inspect"
	"github.com/podhmo/minigo/resolve"
	"github.com/podhmo/minigo/runtime"
)

const handlerPath = "github.com/podhmo/minigo/experiments/httpinspect/testdata/handlers"
const helperPath = "github.com/podhmo/minigo/experiments/httpinspect/testdata/helpers"

func TestAnalyze(t *testing.T) {
	for _, tt := range []struct {
		name       string
		want       []string
		diagnostic string
	}{
		{"Direct", []string{"header:X-Token:string:false", "path:id:string:true", "query:search:string:false"}, ""},
		{"Helper", []string{"query:limit:integer:false"}, ""},
		{"Branch", []string{"header:X-Local:string:false", "query:after:string:false", "query:left:string:false", "query:right:string:false"}, "binary expression"},
		{"Recursive", []string{"query:before-depth:string:false"}, "depth limit"},
		{"Dynamic", []string{"header:X-Key:string:false"}, "dynamic query"},
		{"Loop", nil, "statement unsupported: ForStmt"},
		{"Opaque", []string{"query:opaque:string:false"}, "opaque call: strings.TrimSpace"},
		{"ShadowImport", []string{"header:X-Shadow:string:false"}, ""},
		{"IfShadow", []string{"header:X-Inner:string:false", "query:inside:string:false", "query:outer:string:false"}, "binary expression"},
		{"NamedResult", []string{"query:named:integer:false"}, ""},
		{"UnknownMutation", nil, "statement unsupported: ForStmt"},
		{"Closure", nil, "expression unsupported: FuncLit"},
		{"BranchResult", []string{"query:early:integer:false", "query:late:integer:false"}, "binary expression"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := minigo.NewEngine("../..")
			p, err := e.Package(context.Background(), handlerPath)
			if err != nil {
				t.Fatal(err)
			}
			r, err := httpinspect.Analyze(context.Background(), e, inspect.NewDecl(p, p.Index.Funcs[tt.name]), httpinspect.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, param := range r.Parameters {
				req := "false"
				if param.Required {
					req = "true"
				}
				got = append(got, param.In+":"+param.Name+":"+param.Schema.Type+":"+req)
				if len(param.Evidence) == 0 {
					t.Error("missing evidence")
				}
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("parameters (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.diagnostic != "", r.Incomplete); diff != "" {
				t.Errorf("incomplete: %s", diff)
			}
			if tt.diagnostic != "" {
				found := false
				for _, d := range r.Diagnostics {
					if strings.Contains(d.Reason, tt.diagnostic) {
						found = true
					}
				}
				if !found {
					t.Errorf("missing diagnostic %q: %+v", tt.diagnostic, r.Diagnostics)
				}
			}
			if diff := cmp.Diff(runtime.Indexed, p.State()); diff != "" {
				t.Errorf("target executed: %s", diff)
			}
			if tt.name == "Helper" {
				hp, err := e.Package(context.Background(), helperPath)
				if err != nil {
					t.Fatal(err)
				}
				if diff := cmp.Diff(runtime.Indexed, hp.State()); diff != "" {
					t.Errorf("helper executed: %s", diff)
				}
				found := false
				for _, call := range r.Calls {
					if call.Target == helperPath+".Identity" {
						if diff := cmp.Diff([]string{"parameter@query:limit"}, call.Results); diff != "" {
							t.Error(diff)
						}
						found = true
					}
				}
				if !found {
					t.Error("missing helper result trace")
				}
			}
		})
	}
}
func TestBudget(t *testing.T) {
	e := minigo.NewEngine("../..")
	p, err := e.Package(context.Background(), handlerPath)
	if err != nil {
		t.Fatal(err)
	}
	r, err := httpinspect.Analyze(context.Background(), e, inspect.NewDecl(p, p.Index.Funcs["Helper"]), httpinspect.Limits{Steps: 2})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(true, r.Incomplete); diff != "" {
		t.Error(diff)
	}
	if diff := cmp.Diff(2, r.Steps); diff != "" {
		t.Error(diff)
	}
}

// A resolver spy verifies that summaries stop before stdlib source traversal.
type spyResolver struct {
	inner   resolve.Resolver
	located []string
}

func (s *spyResolver) Locate(ctx context.Context, dir, path string) (*resolve.PackageMeta, error) {
	s.located = append(s.located, path)
	return s.inner.Locate(ctx, dir, path)
}
func (s *spyResolver) LocateDir(ctx context.Context, dir string) (*resolve.PackageMeta, error) {
	return s.inner.LocateDir(ctx, dir)
}
func TestLazyBoundaries(t *testing.T) {
	inner, err := resolve.NewGoScanResolver("../..", resolve.BuildConfig{})
	if err != nil {
		t.Fatal(err)
	}
	spy := &spyResolver{inner: inner}
	e := minigo.NewEngine("../..").WithResolver(spy)
	p, err := e.Package(context.Background(), handlerPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = httpinspect.Analyze(context.Background(), e, inspect.NewDecl(p, p.Index.Funcs["Helper"]), httpinspect.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{handlerPath, helperPath}, spy.located); diff != "" {
		t.Errorf("unexpected import traversal: %s", diff)
	}
}
func TestConcreteExecutionBaseline(t *testing.T) {
	e := minigo.NewEngine("../..")
	_, err := e.Run(context.Background(), handlerPath, "Helper")
	if err == nil || !strings.Contains(err.Error(), "handler init must never execute") {
		t.Fatalf("expected init side effect at concrete execution boundary, got %v", err)
	}
}
