package httpinspect_test

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/experiments/httpinspect"
	"github.com/podhmo/minigo/inspect"
)

// The same source fixtures run under Go as an independent oracle for binding,
// receiver copies, capture writes, and evaluation order. Only init panics and
// the request boundary are replaced: Query records its key and returns "1".
// Native Go still executes every method/closure/assignment in the fixtures.
func TestNativeCallableSemantics(t *testing.T) {
	names := []string{"LocalMethod", "ImportedMethod", "MethodValues", "MethodExpression", "PointerMethodExpression", "PointerReassign", "AddressReassign", "PointerValueReassign", "CallableAlternatives", "AssignmentOrder", "MethodEvaluationOrder", "KnownInterface", "PointerValueMethodExpression", "ArgumentCopyOrder", "PointerWrite", "ReceiverCopy", "ClosureReassign", "ClosureWrite", "ClosureShadow", "EscapedClosure", "NestedClosure", "ReceiverClosure", "BranchCapture", "BranchCallable", "ClosureReturnEffects"}
	dir := t.TempDir()
	write := func(path string, src []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, src, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "go.mod"), []byte("module oracle\n\ngo 1.26.0\n"))
	for _, path := range []string{"helpers/helpers.go", "handlers/handlers.go", "handlers/callables.go"} {
		write(filepath.Join(dir, path), nativeFixture(t, filepath.Join("testdata", path), strings.HasPrefix(path, "helpers/")))
	}
	var main strings.Builder
	main.WriteString(`package main
import("encoding/json"; "net/http"; "os"; "sort"; "oracle/handlers"; "oracle/helpers")
func main(){out:=map[string][]string{}
`)
	for _, name := range names {
		main.WriteString("helpers.Observed=nil\nhandlers." + name + "(nil,nil)\nhandlers." + name + "(nil,&http.Request{})\n")
		main.WriteString("{seen:=map[string]bool{}; var keys []string; for _,key:=range helpers.Observed {if !seen[key]{keys=append(keys,key);seen[key]=true}};sort.Strings(keys);out[" + strconv.Quote(name) + "]=keys}\n")
	}
	main.WriteString("if err:=json.NewEncoder(os.Stdout).Encode(out);err!=nil{panic(err)}}")
	write(filepath.Join(dir, "main.go"), []byte(main.String()))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", "./")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		t.Fatalf("native oracle: %v\n%s", err, stderr.String())
	}
	var native map[string][]string
	if err := json.Unmarshal(data, &native); err != nil {
		t.Fatal(err)
	}
	e := minigo.NewEngine("../..")
	p, err := e.Package(context.Background(), handlerPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		r, err := httpinspect.Analyze(context.Background(), e, inspect.NewDecl(p, p.Index.Funcs[name]), httpinspect.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		var inferred []string
		for _, param := range r.Parameters {
			inferred = append(inferred, param.Name)
		}
		sort.Strings(inferred)
		uncertain := name == "AssignmentOrder" || name == "MethodEvaluationOrder" || name == "ArgumentCopyOrder"
		if uncertain {
			var missing []string
			for _, observed := range native[name] {
				found := false
				for _, candidate := range inferred {
					if observed == candidate {
						found = true
					}
				}
				if !found {
					missing = append(missing, observed)
				}
			}
			if diff := cmp.Diff([]string(nil), missing); diff != "" {
				t.Errorf("%s native observations missing: %s", name, diff)
			}
			if diff := cmp.Diff(true, r.Incomplete); diff != "" {
				t.Errorf("%s missing order uncertainty: %s", name, diff)
			}
		} else if diff := cmp.Diff(native[name], inferred); diff != "" {
			t.Errorf("%s native vs abstract (-native +abstract):\n%s", name, diff)
		}
	}
}

func nativeFixture(t *testing.T, path string, hook bool) []byte {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var decls []ast.Decl
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "init" {
			continue
		}
		decls = append(decls, decl)
	}
	f.Decls = decls
	for _, im := range f.Imports {
		if im.Path.Value == strconv.Quote(helperPath) {
			im.Path.Value = strconv.Quote("oracle/helpers")
		}
	}
	if hook {
		boundary, err := parser.ParseFile(fset, "boundary.go", `package helpers
func Query(r *web.Request,key string) string { Observed=append(Observed,key);return "1" }
`, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "Query" {
				fn.Body = boundary.Decls[0].(*ast.FuncDecl).Body
			}
		}
	}
	var out bytes.Buffer
	if err := printer.Fprint(&out, fset, f); err != nil {
		t.Fatal(err)
	}
	if hook {
		out.WriteString("\nvar Observed []string\n")
	}
	return out.Bytes()
}
