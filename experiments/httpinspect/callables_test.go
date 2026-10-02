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

func TestMethodsAndClosures(t *testing.T) {
	for _, tt := range []struct {
		name       string
		want       []string
		diagnostic string
	}{
		{"LocalMethod", []string{"local-method:string"}, ""},
		{"ImportedMethod", []string{"method:integer"}, ""},
		{"MethodValues", []string{"new:string", "old:string"}, ""},
		{"MethodExpression", []string{"expression:string"}, ""},
		{"PointerMethodExpression", []string{"pointer-expression:string"}, ""},
		{"PointerReassign", []string{"new-variable:string"}, ""},
		{"AddressReassign", []string{"new-address:string"}, ""},
		{"PointerValueReassign", []string{"old-pointer:string"}, ""},
		{"CallableAlternatives", []string{"left-call:string", "right-call:string"}, "binary expression"},
		{"AssignmentOrder", []string{"first:string", "second:string", "written-first:string"}, "evaluation order"},
		{"MethodEvaluationOrder", []string{"after-argument:string", "before-argument:string"}, "evaluation order"},
		{"KnownInterface", []string{"known-interface:string"}, ""},
		{"NonlocalReceiver", nil, "automatic address of nonlocal receiver unsupported"},
		{"PointerValueMethodExpression", []string{"pointer-value-expression:string"}, ""},
		{"ArgumentCopyOrder", []string{"after-copy:string", "before-copy:string"}, "evaluation order"},
		{"PointerWrite", []string{"renamed:string"}, ""},
		{"ReceiverCopy", []string{"copy:string", "original:string"}, ""},
		{"ClosureReassign", []string{"after:string"}, ""},
		{"ClosureWrite", []string{"written:string"}, ""},
		{"ClosureShadow", []string{"inner:string", "outer:string"}, ""},
		{"EscapedClosure", []string{"escaped:integer"}, ""},
		{"NestedClosure", []string{"nested:string"}, ""},
		{"ReceiverClosure", []string{"bound:string"}, ""},
		{"BranchCapture", []string{"left:string", "right:string"}, "binary expression"},
		{"BranchCallable", []string{"left-fn:string", "right-fn:string"}, "binary expression"},
		{"ClosureReturnEffects", []string{"early:string", "late:string"}, "binary expression"},
		{"RecursiveClosure", []string{"recursive-closure:string"}, "call depth limit"},
		{"InterfaceUnknown", nil, "opaque method or field"},
		{"Embedded", nil, "embedded fields unsupported"},
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
			for _, p := range r.Parameters {
				got = append(got, p.Name+":"+p.Schema.Type)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("parameters (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.diagnostic != "", r.Incomplete); diff != "" {
				t.Errorf("incomplete: %s; diagnostics=%+v", diff, r.Diagnostics)
			}
			if tt.diagnostic != "" {
				found := false
				for _, d := range r.Diagnostics {
					if strings.Contains(d.Reason, tt.diagnostic) {
						found = true
					}
				}
				if diff := cmp.Diff(true, found); diff != "" {
					t.Errorf("missing %q: %+v", tt.diagnostic, r.Diagnostics)
				}
			}
			if diff := cmp.Diff(runtime.Indexed, p.State()); diff != "" {
				t.Error(diff)
			}
			if tt.name == "MethodValues" {
				want := map[string][]string{helperPath + ".Reader.Read": {"parameter@query:old"}, helperPath + ".Reader.PointerRead": {"parameter@query:new"}}
				got := map[string][]string{}
				for _, call := range r.Calls {
					if _, ok := want[call.Target]; ok {
						got[call.Target] = call.Results
					}
				}
				if diff := cmp.Diff(want, got); diff != "" {
					t.Errorf("receiver snapshot: %s", diff)
				}
			}
			if tt.name == "ClosureReassign" {
				found := false
				for _, call := range r.Calls {
					for _, capture := range call.Captures {
						if capture == "key=literal:after" {
							found = true
						}
					}
				}
				if diff := cmp.Diff(true, found); diff != "" {
					t.Errorf("missing live captured binding: %s", diff)
				}
			}
		})
	}
}
func TestCallableLazyBoundaries(t *testing.T) {
	for _, name := range []string{"ImportedMethod", "EscapedClosure", "NestedClosure", "ReceiverClosure"} {
		t.Run(name, func(t *testing.T) {
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
			r, err := httpinspect.Analyze(context.Background(), e, inspect.NewDecl(p, p.Index.Funcs[name]), httpinspect.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(false, r.Incomplete); diff != "" {
				t.Error(diff)
			}
			if diff := cmp.Diff([]string{handlerPath, helperPath}, spy.located); diff != "" {
				t.Errorf("unexpected traversal: %s", diff)
			}
			hp, err := e.Package(context.Background(), helperPath)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(runtime.Indexed, hp.State()); diff != "" {
				t.Error(diff)
			}
		})
	}
}
