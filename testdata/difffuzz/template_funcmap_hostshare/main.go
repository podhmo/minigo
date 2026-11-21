package main

import (
	"bytes"
	"fmt"
	"math"
	"net/url"
	"text/template"
)

type nopW struct{}

func (nopW) Write(p []byte) (int, error) { fmt.Print(string(p)); return len(p), nil }

func main() {
	var buf bytes.Buffer
	buf.WriteString("hello")
	template.Must(template.New("t").Funcs(template.FuncMap{
		"up": func(b []byte) string { b[0] = 'H'; return string(b) + "|" },
	}).Parse(`{{up .B}}`)).Execute(&buf, map[string]any{"B": buf.Bytes()})
	fmt.Println(buf.String())

	v := url.Values{"a": {"1", "2"}, "b": {"x"}}
	template.Must(template.New("t").Funcs(template.FuncMap{
		"enc": func(v url.Values) string { return v.Encode() },
	}).Parse(`{{enc .}}`)).Execute(nopW{}, v)
	fmt.Println()

	m := map[float64]string{math.NaN(): "nanval"}
	template.Must(template.New("t").Funcs(template.FuncMap{
		"vals": func(m map[float64]string) string {
			out := ""
			for k, v := range m {
				out += fmt.Sprintf("%v=%s", k, v)
			}
			return out
		},
	}).Parse(`{{vals .}}`)).Execute(nopW{}, m)
	fmt.Println()
}
