package main

// html/template (interpreted from source, see SRC) ranges over an
// intrinsic iter.Seq and a script one, escaping each element.

import (
	"html/template"
	"iter"
	"maps"
	"os"
)

func gen() iter.Seq[string] {
	return func(yield func(string) bool) {
		for _, s := range []string{"<i>", "&"} {
			if !yield(s) {
				return
			}
		}
	}
}

func run(t *template.Template, data any) {
	if err := t.Execute(os.Stdout, data); err != nil {
		os.Stdout.WriteString("error: " + err.Error() + "\n")
	}
}

func main() {
	h := template.Must(template.New("h").Parse("{{range .S}}<b>{{.}}</b>{{end}}\n"))
	run(h, map[string]any{"S": maps.Keys(map[string]bool{"<a>": true})})
	run(h, map[string]any{"S": gen()})
	h2 := template.Must(template.New("h2").Parse("{{range $k, $v := .}}<p title=\"{{$k}}\">{{$v}}</p>{{end}}\n"))
	run(h2, maps.All(map[string]int{"x\"y": 1}))
}
