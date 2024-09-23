package interpreter_test

import (
	"bufio"
	"bytes"
	"context"
	"go/parser"
	"go/token"
	"os"
	"path"
	"strings"
	"testing"

	"github.com/podhmo/minigo/internal/interpreter"
)

func normalize(s string) string {
	return strings.TrimSpace(s)
}

// readOutputComment reads the comment lines after "// Output:" from the file.
func readOutputComment(t *testing.T, filename string) []string {
	f, err := os.Open(filename)
	if err != nil {
		t.Errorf("open file: %+v", err)
		return nil
	}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if line := scanner.Text(); strings.Contains(line, "// Output:") {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Errorf("scan file: %+v", err)
		return nil
	}
	var output []string
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); strings.HasPrefix(line, "//") {
			output = append(output, strings.TrimLeft(line, "/ "))
		} else {
			break
		}
	}
	return output
}

func TestRunFile(t *testing.T) {
	type testcase struct {
		filename   string
		entrypoint string
		output     []string
	}
	for _, c := range []testcase{
		{
			filename: "./testdata/hello.go", entrypoint: "main",
			output: readOutputComment(t, "./testdata/hello.go"),
		},
		{
			filename: "./testdata/another-entrypoint.go", entrypoint: "Foo",
			output: []string{"Foo"},
		},
		{
			filename: "./testdata/another-entrypoint.go", entrypoint: "Bar",
			output: []string{"Bar"},
		},
		{
			filename: "./testdata/use-stdlib.go", entrypoint: "main",
			output: readOutputComment(t, "./testdata/use-stdlib.go"),
		},
		{
			filename: "./testdata/assign.go", entrypoint: "main",
			output: readOutputComment(t, "./testdata/assign.go"),
		},
	} {
		t.Run(path.Base(c.filename), func(t *testing.T) {
			ctx := context.Background()
			fset := token.NewFileSet()

			stdout := new(bytes.Buffer)
			app := interpreter.New(fset,
				interpreter.WithStderr(stdout),
				interpreter.WithStdout(stdout),
			)

			node, err := parser.ParseFile(fset, c.filename, nil, parser.AllErrors)
			if err != nil {
				t.Fatalf("parse file: +%v", err)
			}
			if err := app.RunFile(ctx, node, c.entrypoint); err != nil {
				t.Errorf("run file: %+v", err)
			}

			if want, got := normalize(strings.Join(c.output, "\n")), normalize(stdout.String()); want != got {
				t.Errorf("want:\n\t%q\nbut got:\n\t%q", want, got)
			}
		})
	}
}
