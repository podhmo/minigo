package main

import (
	"bytes"
	"fmt"
	"os"
	"text/template"
	"text/template/parse"
)

func must(t *template.Template, err error) *template.Template {
	if err != nil {
		panic(err)
	}
	return t
}
func cached(name, text string, funcs template.FuncMap) *template.Template {
	return must(template.New(name).Funcs(funcs).ExperimentParseCached(text))
}
func execute(t *template.Template, name string, data any) (string, error) {
	var b bytes.Buffer
	err := t.ExecuteTemplate(&b, name, data)
	return b.String(), err
}
func expect(t *template.Template, name string, data any, want string) {
	got, err := execute(t, name, data)
	if err != nil || got != want {
		panic(fmt.Sprintf("%s got=%q want=%q err=%v", name, got, want, err))
	}
}
func main() {
	calls := 0
	funcs := template.FuncMap{"value": func() string { calls++; return "one" }}
	a := cached("functions", "{{value}}", funcs)
	if calls != 0 {
		panic("parse invoked a function")
	}
	expect(a, "functions", nil, "one")
	b := cached("functions", "{{value}}", template.FuncMap{"value": func() string { return "two" }})
	expect(b, "functions", nil, "two")
	_, err := template.New("functions").ExperimentParseCached("{{value}}")
	if err == nil {
		panic("function-name key was ignored")
	}
	fmt.Println("missing-function:", err)

	a = cached("independent", "original", nil)
	b = cached("independent", "original", nil)
	a.Tree.Root.Nodes[0].(*parse.TextNode).Text[0] = 'X'
	expect(a, "independent", nil, "Xriginal")
	expect(b, "independent", nil, "original")
	c := cached("independent", "original", nil)
	expect(c, "independent", nil, "original")
	clone := must(a.Clone())
	a.Tree.Root.Nodes[0].(*parse.TextNode).Text[1] = 'Y'
	expect(clone, "independent", nil, "XYiginal")

	a = cached("definitions", "{{define \"child\"}}first{{end}}{{template \"child\"}}", nil)
	must(a.ExperimentParseCached("{{define \"child\"}}second{{end}}"))
	expect(a, "definitions", nil, "second")
	must(a.ExperimentParseCached("{{define \"child\"}}  {{/* empty */}}{{end}}"))
	expect(a, "definitions", nil, "second")
	clone = must(a.Clone())
	must(clone.ExperimentParseCached("{{define \"child\"}}third{{end}}"))
	expect(clone, "definitions", nil, "third")
	expect(a, "definitions", nil, "second")

	d := template.New("delimiters").Delims("[[", "]]")
	must(d.ExperimentParseCached("[[if .]]yes[[else]]no[[end]]"))
	expect(d, "delimiters", true, "yes")
	a = cached("numeric", "{{printf \"%v/%v/%v/%v\" 0xffffffffffffffff 3.5 2i '界'}}", nil)
	got, err := execute(a, "numeric", nil)
	fmt.Printf("numeric: %q err=%v\n", got, err)
	valid := cached("numeric-valid", "{{printf \"%v/%v/%v/%v/%v\" 42 3.5 2i '界' true}}", nil)
	got, err = execute(valid, "numeric-valid", nil)
	if err != nil {
		panic(err)
	}
	fmt.Printf("numeric-valid: %q\n", got)

	capacity := cached("capacity", "a{{.}}b", nil)
	fmt.Printf("nodes-capacity: %d/%d\n", len(capacity.Tree.Root.Nodes), cap(capacity.Tree.Root.Nodes))
	alias := cached("slices", "a{{.}}b", nil)
	other := cached("slices", "a{{.}}b", nil)
	short := alias.Tree.Root.Nodes[:1]
	short = append(short, other.Tree.Root.Nodes[0])
	expect(alias, "slices", "D", "aab")
	expect(other, "slices", "D", "aDb")
	old := alias.Tree.Root.Nodes
	alias.Tree.Root.Nodes = other.Tree.Root.Nodes
	old[0] = other.Tree.Root.Nodes[2]
	expect(alias, "slices", "D", "aDb")
	expect(other, "slices", "D", "aDb")
	a = cached("positions", "line one\n{{define \"child\"}}{{.Missing}}{{end}}\n{{template \"child\" .}}", nil)
	_, err = execute(a, "positions", struct{}{})
	if err == nil {
		panic("missing execution error")
	}
	fmt.Println("execution-error:", err)
	_, err = template.New("broken").ExperimentParseCached("{{if .}}")
	if err == nil {
		panic("missing syntax error")
	}
	fmt.Println("syntax-error:", err)
	_, err = template.New("duplicate").ExperimentParseCached("{{define \"x\"}}a{{end}}{{define \"x\"}}b{{end}}")
	if err == nil {
		panic("missing duplicate definition error")
	}
	fmt.Println("duplicate-error:", err)
	fmt.Fprintln(os.Stdout, "semantic probes passed")
}
