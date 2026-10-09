package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/template"
)

// An interpreted text/template over an interpreted text/template/parse
// whose lexer runs on the host (internal/tmpllex): the parser consumes
// host-lexed items and exec walks script parse trees.

type Item struct {
	Name  string
	Price float64
	Tags  []string
	Next  *Item
}

func (i Item) Upper() string              { return strings.ToUpper(i.Name) }
func (i Item) Join(sep string) string     { return strings.Join(i.Tags, sep) }
func (i *Item) HasNext() bool             { return i.Next != nil }
func (i Item) Pair(a, b int) (int, error) { return a + b, nil }

const page = `{{/* a comment */ -}}
{{define "item"}}[{{.Name}} {{printf "%.2f" .Price}}{{if .Tags}} {{.Join ","}}{{end}}]{{end -}}
{{define "list"}}{{range $i, $it := .}}{{if $i}}, {{end}}{{template "item" $it}}{{end}}{{end -}}
Items: {{template "list" .Items}}
{{with .Missing}}has missing{{else}}no missing{{end}}
{{range $k, $v := .Counts}}{{$k}}={{$v}};{{end}}
{{range .Nums}}{{if eq . 3}}{{continue}}{{end}}{{if gt . 5}}{{break}}{{end}}{{.}} {{end}}
{{range $i, $e := .Empty}}never{{else}}empty range{{end}}
{{$x := 1}}{{$x = add $x 41}}x={{$x}} {{len .Items}} {{index .Nums 2}} {{slice "abcdef" 1 3}}
{{and 1 0 2}} {{or 0 "" "z"}} {{not true}} {{lt 1 2}} {{ne "a" "b"}} {{le 2.5 2.5}}
{{.First.Upper}} {{.First.Next.Name}} {{.First.HasNext}} {{.First.Pair 2 3}}
{{0x1F}} {{1e3}} {{'a'}} {{-7}} {{3.5}} {{"q" | printf "%s-%s" "p"}}
{{- /* trim */ -}}
{{block "footer" .}} default footer {{.Title | shout}}{{end}}
{{.Map.key}} {{.Map.missing}}
`

func main() {
	funcs := template.FuncMap{
		"add":   func(a, b int) int { return a + b },
		"shout": func(s string) string { return strings.ToUpper(s) + "!" },
	}
	t := template.Must(template.New("page").Funcs(funcs).Parse(page))
	b := &Item{Name: "bolt", Price: 0.5}
	data := map[string]any{
		"Items":  []*Item{{Name: "nut", Price: 1.25, Tags: []string{"m3", "steel"}, Next: b}, b},
		"Counts": map[string]int{"b": 2, "a": 1},
		"Nums":   []int{1, 2, 3, 4, 5, 6, 7},
		"Empty":  []int{},
		"First":  &Item{Name: "nut", Next: b},
		"Title":  "done",
		"Map":    map[string]string{"key": "val"},
	}
	if err := t.Execute(os.Stdout, data); err != nil {
		fmt.Println("exec error:", err)
	}

	// clone and override a block, as oapi-codegen's per-framework hooks do
	c := template.Must(t.Clone())
	template.Must(c.Parse(`{{define "footer"}}custom footer{{end}}`))
	var sb strings.Builder
	if err := c.ExecuteTemplate(&sb, "footer", data); err != nil {
		fmt.Println("exec error:", err)
	}
	fmt.Println("clone:", sb.String())

	var names []string
	for _, x := range t.Templates() {
		names = append(names, x.Name())
	}
	sort.Strings(names)
	fmt.Println("templates:", names)
	fmt.Println("lookup:", t.Lookup("item") != nil, t.Lookup("nope") == nil)

	// errors: parse, unknown function, missing template, bad field
	_, err := template.New("bad").Parse("{{if}}")
	fmt.Println("parse error:", err)
	_, err = template.New("nofunc").Parse("{{nosuch 1}}")
	fmt.Println("func error:", err)
	err = template.Must(template.New("x").Parse(`{{template "zzz"}}`)).Execute(&sb, nil)
	fmt.Println("missing template:", err)
	err = template.Must(template.New("y").Parse(`{{.Nope}}`)).Execute(&sb, Item{})
	fmt.Println("bad field:", err)
	err = template.Must(template.New("z").Option("missingkey=error").Parse(`{{.k}}`)).Execute(&sb, map[string]int{})
	fmt.Println("missingkey:", err)
}
