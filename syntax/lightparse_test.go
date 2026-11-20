package syntax

import (
	"go/token"
	"testing"

	"github.com/google/go-cmp/cmp"
)

var lightSrc = `// comment
package sample

import (
	"fmt"
	renamed "os"
	. "strings"
	_ "embed"
	"github.com/x/y/v2"
)

func Body() { fmt.Println("never parsed") }
`

func TestParsePackageClause(t *testing.T) {
	name, err := ParsePackageClause("sample.go", []byte(lightSrc))
	if err != nil {
		t.Fatal(err)
	}
	if name != "sample" {
		t.Fatalf("name = %q", name)
	}
	if _, err := ParsePackageClause("bad.go", []byte("func f(){}")); err == nil {
		t.Error("want error for a missing package clause")
	}
}

func TestParseImports(t *testing.T) {
	name, imports, err := ParseImports(token.NewFileSet(), "sample.go", []byte(lightSrc))
	if err != nil {
		t.Fatal(err)
	}
	if name != "sample" {
		t.Fatalf("name = %q", name)
	}
	var got []string
	for _, imp := range imports {
		got = append(got, imp.Alias+":"+imp.Path)
	}
	want := []string{":fmt", "renamed:os", ".:strings", "_:embed", ":github.com/x/y/v2"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("imports (-want +got):\n%s", diff)
	}
}

func TestParseImports_BrokenBody(t *testing.T) {
	// body syntax is never scanned: no error, imports still returned
	src := "package p\n\nimport \"fmt\"\n\nfunc f() { ~~~"
	name, imports, err := ParseImports(token.NewFileSet(), "b.go", []byte(src))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if name != "p" || len(imports) != 1 || imports[0].Path != "fmt" {
		t.Fatalf("header = %q, %v", name, imports)
	}

	// a malformed header surfaces the error (never a silent empty table)
	if _, _, err := ParseImports(token.NewFileSet(), "b.go", []byte("package p\n\nimport 123\n")); err == nil {
		t.Fatal("want the header error attached")
	}
}
