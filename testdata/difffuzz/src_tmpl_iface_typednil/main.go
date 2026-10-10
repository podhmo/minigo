package main

import (
	"os"
	"text/template"
)

type V struct{}

func (v *V) String() string {
	if v == nil {
		return "nilV"
	}
	return "V"
}

func main() {
	data := struct {
		Z  *int
		V2 *V
	}{Z: nil, V2: nil}
	template.Must(template.New("t").Parse("-{{.Z}}--{{.V2}}-")).Execute(os.Stdout, data)
}
