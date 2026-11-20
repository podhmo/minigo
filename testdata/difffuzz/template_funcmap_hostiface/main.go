package main

import (
	"fmt"
	"net/url"
	"text/template"
)

type nopW struct{}

func (nopW) Write(p []byte) (int, error) { fmt.Print(string(p)); return len(p), nil }

func main() {
	v := url.Values{"a": {"1"}}
	err := template.Must(template.New("t").Funcs(template.FuncMap{
		"enci": func(e interface{ Encode() string }) string { return e.Encode() },
	}).Parse(`{{enci .}}`)).Execute(nopW{}, v)
	fmt.Println(" ERR:", err)
}
