package main

// json.Marshal of a seq or any other func value fails with go's
// "unsupported type" naming the script type (iter.Seq[string],
// func(int) int), not the interpreter's internals; a host template's
// %T of a seq over basic elements spells the real iter.Seq type.

import (
	"encoding/json"
	"fmt"
	"iter"
	"maps"
	"os"
	"slices"
	"text/template"
)

type S struct {
	F func(int) int
}

func gen() iter.Seq[int] {
	return func(yield func(int) bool) {}
}

func main() {
	_, err := json.Marshal(maps.Keys(map[string]bool{"a": true}))
	fmt.Println(err)
	_, err = json.Marshal(map[string]any{"s": gen()})
	fmt.Println(err)
	_, err = json.Marshal(maps.All(map[string]int{}))
	fmt.Println(err)
	_, err = json.Marshal(func(int) int { return 0 })
	fmt.Println(err)
	_, err = json.Marshal(S{F: func(int) int { return 0 }})
	fmt.Println(err)
	_, err = json.MarshalIndent([]any{maps.Values(map[int]bool{})}, "", " ")
	fmt.Println(err)
	t := template.Must(template.New("t").Parse("{{printf \"%T\" .}}\n"))
	t.Execute(os.Stdout, maps.Keys(map[string]bool{"a": true}))
	t.Execute(os.Stdout, gen())
	t.Execute(os.Stdout, slices.All([]float64{1}))
	t.Execute(os.Stdout, maps.All(map[string]uint8{}))
}
