// Package main registers script funcs with text/template's and
// html/template's FuncMap.
package main

import (
	"fmt"
	htmpl "html/template"
	"strings"
	"text/template"
)

type user struct{ Name string }

func shout(s string) string { return strings.ToUpper(s) + "!" }

// Text registers a bound host func, a declared script func, a variadic
// closure and an int-typed closure with text/template.
func Text() string {
	var sb strings.Builder
	t := template.Must(template.New("t").Funcs(template.FuncMap{
		"up":    strings.ToUpper,
		"shout": shout,
		"join":  func(sep string, xs ...string) string { return strings.Join(xs, sep) },
		"add":   func(a, b int) int { return a + b },
	}).Parse(`{{up .Name}} {{shout .Name}} {{join "-" "a" "b"}} {{add 1 2}}`))
	if err := t.Execute(&sb, user{Name: "bob"}); err != nil {
		return "error: " + err.Error()
	}
	return sb.String()
}

// Fail returns a script func's error through the template.
func Fail() string {
	t := template.Must(template.New("t").Funcs(template.FuncMap{
		"fail": func() (string, error) { return "", fmt.Errorf("boom") },
	}).Parse(`{{fail}}`))
	var sb strings.Builder
	err := t.Execute(&sb, nil)
	return fmt.Sprint(err)
}

// HTML returns template.HTML from a script func under html/template.
func HTML() string {
	var sb strings.Builder
	t := htmpl.Must(htmpl.New("h").Funcs(htmpl.FuncMap{
		"badge": func(s string) htmpl.HTML { return htmpl.HTML("<b>" + htmpl.HTMLEscapeString(s) + "</b>") },
		"lower": strings.ToLower,
	}).Parse(`<p>{{badge .Name}} {{lower .Name}}</p>`))
	if err := t.Execute(&sb, user{Name: "<Bob>"}); err != nil {
		return "error: " + err.Error()
	}
	return sb.String()
}

type item struct {
	Tags []string
	Meta map[string]string
}

// NilFields passes nil slice and map fields of a script struct to funcs
// typed for them.
func NilFields() string {
	var sb strings.Builder
	t := template.Must(template.New("n").Funcs(template.FuncMap{
		"join": strings.Join,
		"size": func(m map[string]string) int { return len(m) },
	}).Parse(`[{{join .Tags ","}}] {{size .Meta}}`))
	if err := t.Execute(&sb, item{}); err != nil {
		return "error: " + err.Error()
	}
	return sb.String()
}

func main() {
	fmt.Println(Text())
	fmt.Println(Fail())
	fmt.Println(HTML())
	fmt.Println(NilFields())
}
