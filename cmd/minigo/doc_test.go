package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo"
)

func TestDocTarget(t *testing.T) {
	ctx := context.Background()
	cwd, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	r := minigo.NewEngine(cwd).NewREPL()
	for _, line := range []string{`import foo "encoding/json"`, `import "./testdata/inspectpkg"`} {
		if _, err := r.EvalLine(ctx, line); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
	}
	inspectpkg := filepath.Join(cwd, "testdata/inspectpkg")

	type target struct {
		Dir  string
		Args []string
	}
	cases := []struct {
		arg  string
		want target
	}{
		{"foo", target{cwd, []string{"encoding/json"}}},
		{"foo.Unmarshal", target{cwd, []string{"encoding/json", "Unmarshal"}}},
		{`"encoding/json"`, target{cwd, []string{"encoding/json"}}},
		{`"encoding/json".Unmarshal`, target{cwd, []string{"encoding/json", "Unmarshal"}}},
		{"encoding/json Unmarshal", target{cwd, []string{"encoding/json", "Unmarshal"}}},
		{"encoding/json.Unmarshal", target{cwd, []string{"encoding/json.Unmarshal"}}},
		{"inspectpkg.User.Greet", target{inspectpkg, []string{".", "User.Greet"}}},
		{`"./testdata/inspectpkg" Hello`, target{inspectpkg, []string{".", "Hello"}}},
	}
	for _, c := range cases {
		dir, args, err := docTarget(r, cwd, c.arg)
		if err != nil {
			t.Errorf("docTarget(%s): %v", c.arg, err)
			continue
		}
		if diff := cmp.Diff(c.want, target{dir, args}); diff != "" {
			t.Errorf("docTarget(%s) (-want +got):\n%s", c.arg, diff)
		}
	}
	if _, _, err := docTarget(r, cwd, " "); err == nil {
		t.Error("docTarget: empty argument should be a usage error")
	}
}

func TestRunREPLDoc(t *testing.T) {
	in := strings.NewReader("import foo \"encoding/json\"\n:doc foo.Valid\n:exit\n")
	var out bytes.Buffer
	if err := runREPL(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "func Valid(data []byte) bool") {
		t.Errorf("output missing go doc for Valid\n---\n%s", got)
	}
}
