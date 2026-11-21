package main

import (
	"fmt"
	"net/url"
	"os"
	"text/template"
)

func main() {
	v := url.Values{"a": {"1"}, "b": {"2"}}
	var saved url.Values
	t := template.Must(template.New("t").Funcs(template.FuncMap{
		"save":  func(m url.Values) string { saved = m; return "s" },
		"write": func() string { saved.Set("w", "9"); return "w" },
		"get":   func(m url.Values) string { return m.Encode() },
	}).Parse(`{{save .}}{{write}}{{get .}}`))
	if err := t.Execute(os.Stdout, v); err != nil {
		panic(err)
	}
	fmt.Println("|", v.Encode())
}
