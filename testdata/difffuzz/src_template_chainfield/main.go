package main

// --src text/template: a field access chained off a builtin's
// reflect.Value result. The script `index` func hands its result back
// in reflect.Value's script repr (a GoValue boxing the facade value);
// a second wrap would leave the box on the read view and Field would
// trap "call of reflect.Value.Field on struct Value" on a struct.

import (
	"os"
	"text/template"
)

type Item struct{ Name string }

type data struct{ Items []Item }

func run(src string, d data) {
	t := template.Must(template.New("t").Parse(src))
	if err := t.Execute(os.Stdout, d); err != nil {
		os.Stdout.WriteString("ERR " + err.Error())
	}
	os.Stdout.WriteString("\n")
}

func main() {
	d := data{Items: []Item{{Name: "first"}, {Name: "second"}}}
	run(`{{index .Items 1}}`, d)
	run(`{{(index .Items 1).Name}}`, d)
	run(`{{with index .Items 1}}{{.Name}}{{end}}`, d)
	run(`{{$x := index .Items 1}}{{$x.Name}}`, d)
}
