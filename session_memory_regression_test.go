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
	"errors"
	"probe"
	"strconv"
	"strings"
	"text/template"
)

type Item struct{ N int }

type Count int

var errInvalid = errors.New("invalid")

var childCalls int

type Child struct{}

func (c *Child) Label() string { childCalls++; return "x" }

type EffectParent struct{ N int }


type ItemList struct {
	N      int
	Values []int
}

func (p *ItemList) Empty() bool {
	for range p.Values {
		return false
	}
	return true
}


type ShadowParent struct {
	N      int
	Values []int
}

type ShadowBox struct{ Values chan int }

var shadowBox *ShadowBox

func (p *ShadowParent) Read() int {
	{
		var p = shadowBox
		for v := range p.Values {
			return v
		}
	}
	return 0
}

type AssignParent struct {
	N      int
	Values []int
}

var rangeIndex int

func (p *AssignParent) Last() int {
	for rangeIndex = range p.Values {
	}
	return rangeIndex
}

type Stream struct {
	N      int
	Values chan int
}

func (s *Stream) Read() int { return <-s.Values }

func (p *EffectParent) Child() *Child { return &Child{} }

func (p *Item) Label() int { return p.N }

func (p *Item) Count() Count { return Count(p.N) }

func (p *Item) Byte() byte { return byte(p.N) }

func (p *Item) Status() (int, error) {
	if p.N == 8 {
		return 0, errInvalid
	}
	return p.N, nil
}

func Save() {
	probe.Store(&Item{N: 7})
}

func RoundTrip() bool {
	item, ok := probe.Load().(*Item)
	return ok && item.N == 7
}

func renderUpdate(data any, update func() any, expr string) string {
	t := template.Must(template.New("render").Funcs(template.FuncMap{
		"update": update,
	}).Parse("{{with update}}" + expr + "{{end}}"))

	var out strings.Builder
	if err := t.Execute(&out, data); err != nil {
		return err.Error()
	}
	return out.String()
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

func RenderReceive() string {
	values := make(chan int, 2)
	values <- 1
	values <- 2
	item := &Stream{Values: values}
	t := template.Must(template.New("receive").Funcs(template.FuncMap{
		"update": func() any {
			item.N = 8
			return item
		},
	}).Parse("{{with update}}{{.Read}}{{end}}"))

	var out strings.Builder
	if err := t.Execute(&out, item); err != nil {
		return err.Error()
	}
	return out.String()
}

func RenderChildEffect() string {
	item := &EffectParent{N: 7}
	childCalls = 0
	t := template.Must(template.New("child-effect").Funcs(template.FuncMap{
		"update": func() any {
			item.N = 8
			return item
		},
	}).Parse("{{with update}}{{.Child.Label}}{{end}}"))

	var out strings.Builder
	if err := t.Execute(&out, item); err != nil {
		return err.Error()
	}
	return out.String() + "/" + strconv.Itoa(childCalls)
}

func RenderError() string {
	item := &Item{N: 7}
	t := template.Must(template.New("item-error").Funcs(template.FuncMap{
		"update": func() any {
			item.N = 8
			return item
		},
	}).Parse("{{with update}}{{.Status}}{{end}}"))

	var out strings.Builder
	if err := t.Execute(&out, item); err != nil {
		return err.Error()
	}
	return out.String()
}

func RenderNamedScalars() string {
	item := &Item{N: 7}
	t := template.Must(template.New("named-scalars").Funcs(template.FuncMap{
		"update": func() any {
			item.N = 8
			return item
		},
	}).Parse("{{with update}}{{.Count}}/{{.Byte}}{{end}}"))

	var out strings.Builder
	if err := t.Execute(&out, item); err != nil {
		return err.Error()
	}
	return out.String()
}

func RenderSliceRange() string {
	item := &ItemList{}
	t := template.Must(template.New("slice-range").Funcs(template.FuncMap{
		"update": func() any {
			item.N = 8
			item.Values = []int{1}
			return item
		},
	}).Parse("{{with update}}{{.Empty}}{{end}}"))

	var out strings.Builder
	if err := t.Execute(&out, item); err != nil {
		return err.Error()
	}
	return out.String()
}

func RenderShadowedReceive() string {
	values := make(chan int, 2)
	values <- 1
	values <- 2
	shadowBox = &ShadowBox{Values: values}
	item := &ShadowParent{}
	return renderUpdate(item, func() any {
		item.N = 8
		return item
	}, "{{.Read}}")
}

func RenderRangeAssign() string {
	item := &AssignParent{Values: []int{0}}
	rangeIndex = 0
	body := renderUpdate(item, func() any {
		item.N = 8
		item.Values = []int{0, 1}
		rangeIndex = 99
		return item
	}, "{{.Last}}")
	return body + "/" + strconv.Itoa(rangeIndex)
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
	for _, name := range []string{"RoundTrip", "Render", "RenderReceive", "RenderShadowedReceive", "RenderRangeAssign", "RenderNamedScalars", "RenderSliceRange", "RenderError", "RenderChildEffect"} {
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

func TestCallbackReturnedStructMethodErrorsClearStaleResults(t *testing.T) {
	got := runSessionMemoryRegression(t)["RenderError"]
	if diff := cmp.Diff("<no value>", got); diff != "" {
		t.Fatalf("RenderError (-want +got):\n%s", diff)
	}
}

func TestCallbackReturnedCompositeMethodsDoNotDuplicateChildEffects(t *testing.T) {
	got := runSessionMemoryRegression(t)["RenderChildEffect"]
	if diff := cmp.Diff("x/1", got); diff != "" {
		t.Fatalf("RenderChildEffect (-want +got):\n%s", diff)
	}
}

func TestCallbackReturnedReceiveMethodsAreNotRepeated(t *testing.T) {
	got := runSessionMemoryRegression(t)["RenderReceive"]
	if diff := cmp.Diff("1", got); diff != "" {
		t.Fatalf("RenderReceive (-want +got):\n%s", diff)
	}
}

func TestCallbackReturnedNamedScalarMethodsRefresh(t *testing.T) {
	got := runSessionMemoryRegression(t)["RenderNamedScalars"]
	if diff := cmp.Diff("8/8", got); diff != "" {
		t.Fatalf("RenderNamedScalars (-want +got):\n%s", diff)
	}
}

func TestCallbackReturnedSliceRangeMethodsRefresh(t *testing.T) {
	got := runSessionMemoryRegression(t)["RenderSliceRange"]
	if diff := cmp.Diff("false", got); diff != "" {
		t.Fatalf("RenderSliceRange (-want +got):\n%s", diff)
	}
}

func TestCallbackReturnedShadowedReceiveMethodsAreNotRepeated(t *testing.T) {
	got := runSessionMemoryRegression(t)["RenderShadowedReceive"]
	if diff := cmp.Diff("1", got); diff != "" {
		t.Fatalf("RenderShadowedReceive (-want +got):\n%s", diff)
	}
}

func TestCallbackReturnedRangeAssignmentsAreNotRepeated(t *testing.T) {
	got := runSessionMemoryRegression(t)["RenderRangeAssign"]
	if diff := cmp.Diff("0/99", got); diff != "" {
		t.Fatalf("RenderRangeAssign (-want +got):\n%s", diff)
	}
}
