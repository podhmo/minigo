package main

// an iter.Seq / iter.Seq2 handed to the host text/template ranges like
// in go (go1.24+ templates range over iterator funcs): intrinsic seqs
// (maps.Keys/Values/All, strings.SplitSeq, slices.All) and a script
// func returning iter.Seq[int] all cross as real host seq funcs, and a
// {{break}} or an execution error inside the body stops the producer.

import (
	"iter"
	"maps"
	"os"
	"slices"
	"strings"
	"text/template"
)

func gen() iter.Seq[int] {
	return func(yield func(int) bool) {
		for i := range 3 {
			if !yield(i) {
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
	t := template.Must(template.New("t").Parse("{{range .S}}<{{.}}>{{end}}\n"))
	run(t, map[string]any{"S": maps.Keys(map[int]bool{1: true})})
	run(t, map[string]any{"S": maps.Values(map[int]string{1: "v"})})
	run(t, map[string]any{"S": gen()})
	run(t, map[string]any{"S": strings.SplitSeq("a,b", ",")})
	run(t, map[string]any{"S": slices.Values([]string{"x", "y"})})
	t2 := template.Must(template.New("t2").Parse("{{range $k, $v := .S}}<{{$k}}={{$v}}>{{end}}\n"))
	run(t2, map[string]any{"S": maps.All(map[string]int{"x": 1})})
	run(t2, map[string]any{"S": slices.All([]string{"p", "q"})})
	t3 := template.Must(template.New("t3").Parse("{{range .}}{{if eq . 1}}{{break}}{{end}}[{{.}}]{{end}}\n"))
	run(t3, gen())
	t4 := template.Must(template.New("t4").Parse("{{range .}}{{.X}}{{end}}\n"))
	run(t4, gen())
}
