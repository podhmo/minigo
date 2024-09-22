package main

import (
	"bytes"
	"context"
	"go/parser"
	"go/token"
	"io"
	"path"
	"strings"
	"testing"
)

func newApp(fset *token.FileSet, entryPoint string, stdout, stderr io.Writer) *App {
	return &App{
		fset:       fset,
		entryPoint: entryPoint,
		evaluator:  &evaluator{stdout: stdout, stderr: stderr},
	}
}

func normalize(s string) string {
	return strings.TrimSpace(s)
}

func TestHello(t *testing.T) {
	type testcase struct {
		filename   string
		entrypoint string
		output     []string
	}
	for _, c := range []testcase{
		{
			filename: "./internal/e2e/testdata/hello.go", entrypoint: "main",
			output: []string{
				"Hello, World!",
				"Hello World!",
				"Hello World!",
			},
		},
		{
			filename: "./internal/e2e/testdata/another-entrypoint.go", entrypoint: "Foo",
			output: []string{
				"Foo",
			},
		},
		{
			filename: "./internal/e2e/testdata/use-stdlib.go", entrypoint: "main",
			output: []string{
				"HELLO, WORLD",
			},
		},
	} {
		t.Run(path.Base(c.filename), func(t *testing.T) {
			ctx := context.Background()
			fset := token.NewFileSet()

			stdout := new(bytes.Buffer)
			app := newApp(fset, c.entrypoint, stdout, stdout)

			node, err := parser.ParseFile(fset, c.filename, nil, parser.AllErrors)
			if err != nil {
				t.Fatalf("parse file: +%v", err)
			}
			if err := app.runFile(ctx, node); err != nil {
				t.Errorf("run file: %+v", err)
			}

			if want, got := normalize(strings.Join(c.output, "\n")), normalize(stdout.String()); want != got {
				t.Errorf("want:\n\t%q\nbut got:\n\t%q", want, got)
			}
		})
	}
}
