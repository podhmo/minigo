package e2e

import (
	"bytes"
	"context"
	"go/parser"
	"go/token"
	"path"
	"strings"
	"testing"

	"github.com/podhmo/minigo/cmd/minigo/internal/interpreter"
)

func normalize(s string) string {
	return strings.TrimSpace(s)
}

func TestRunFileOutput(t *testing.T) {
	type testcase struct {
		filename   string
		entrypoint string
		output     []string
	}
	for _, c := range []testcase{
		{
			filename: "./testdata/hello.go", entrypoint: "main",
			output: []string{
				"Hello, World!",
				"Hello World!",
				"Hello World!",
			},
		},
		{
			filename: "./testdata/another-entrypoint.go", entrypoint: "Foo",
			output: []string{
				"Foo",
			},
		},
		{
			filename: "./testdata/use-stdlib.go", entrypoint: "main",
			output: []string{
				"HELLO, WORLD",
			},
		},
		{
			filename: "./testdata/assign.go", entrypoint: "main",
			output: []string{
				"before !!",
				"** shadow **",
				"before !!",
				"after !!",
			},
		},
	} {
		t.Run(path.Base(c.filename), func(t *testing.T) {
			ctx := context.Background()
			fset := token.NewFileSet()

			stdout := new(bytes.Buffer)
			app := interpreter.New(fset, c.entrypoint, stdout, stdout)

			node, err := parser.ParseFile(fset, c.filename, nil, parser.AllErrors)
			if err != nil {
				t.Fatalf("parse file: +%v", err)
			}
			if err := app.RunFile(ctx, node); err != nil {
				t.Errorf("run file: %+v", err)
			}

			if want, got := normalize(strings.Join(c.output, "\n")), normalize(stdout.String()); want != got {
				t.Errorf("want:\n\t%q\nbut got:\n\t%q", want, got)
			}
		})
	}
}
