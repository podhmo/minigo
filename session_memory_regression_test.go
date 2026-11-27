package minigo_test

import (
	"context"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo"
	miniruntime "github.com/podhmo/minigo/runtime"
)

const sessionMemoryRegressionScript = `package main

import (
	"probe"
	"strings"
	"text/template"
)

type Item struct{ N int }

func (p *Item) Label() int { return p.N }

func Save() {
	probe.Store(&Item{N: 7})
}

func RoundTrip() bool {
	item, ok := probe.Load().(*Item)
	return ok && item.N == 7
}

func Render() string {
	item := &Item{N: 7}
	t := template.Must(template.New("item").Funcs(template.FuncMap{
		"update": func() any {
			item.N = 8
			return item
		},
	}).Parse("{{with update}}{{.Label}}/{{.N}}{{end}}"))

	var out strings.Builder
	if err := t.Execute(&out, item); err != nil {
		return err.Error()
	}
	return out.String()
}
`

func runSessionMemoryRegression(t *testing.T) map[string]any {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "script.txt")
	if err := os.WriteFile(script, []byte(sessionMemoryRegressionScript), 0o644); err != nil {
		t.Fatal(err)
	}

	var held any
	engine := minigo.NewEngine(".")
	engine.Bind("probe", map[string]miniruntime.Value{
		"Store": &miniruntime.GoValue{V: func(v any) { held = v }},
		"Load":  &miniruntime.GoValue{V: func() any { return held }},
	})

	ctx := context.Background()
	pkg, err := engine.LoadFile(ctx, script)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Call(ctx, pkg, "Save"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		goruntime.GC()
	}

	out := map[string]any{}
	for _, name := range []string{"RoundTrip", "Render"} {
		value, err := engine.Call(ctx, pkg, name)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = value
	}
	return out
}

func TestHostHeldStructProjectionRoundTripsAfterGC(t *testing.T) {
	got := runSessionMemoryRegression(t)["RoundTrip"]
	if diff := cmp.Diff(true, got); diff != "" {
		t.Fatalf("RoundTrip (-want +got):\n%s", diff)
	}
}

func TestCallbackReturnedStructMethodsRefresh(t *testing.T) {
	got := runSessionMemoryRegression(t)["Render"]
	if diff := cmp.Diff("8/8", got); diff != "" {
		t.Fatalf("Render (-want +got):\n%s", diff)
	}
}
