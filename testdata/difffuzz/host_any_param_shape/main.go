package main

import (
	"os"
	"text/template"
	"time"
)

// Script values crossing a host `any` parameter (text/template's
// Execute data) keep their Go type when it has a host equivalent:
// untyped and sized basics, and slices, arrays and maps built from
// basics, bound host types and `any`.

func main() {
	tpl := template.Must(template.New("t").Parse("{{printf \"%T %v\" . .}}\n"))
	for _, v := range []any{5, int(5), int8(-3), int16(7), int32(8), int64(9), uint(1), uint8(2), uint64(1 << 63), uintptr(4),
		1.5, float32(0.1), 'x', byte('y'), "s", true, complex(1, 2), complex64(complex(3, 4))} {
		tpl.Execute(os.Stdout, v)
	}
	tpl.Execute(os.Stdout, 5)
	tpl.Execute(os.Stdout, float32(2.5))
	tpl.Execute(os.Stdout, []int{1, 2})
	tpl.Execute(os.Stdout, []string{"a", "b"})
	tpl.Execute(os.Stdout, []int8{1, -1})
	tpl.Execute(os.Stdout, []float32{0.1, 0.2})
	tpl.Execute(os.Stdout, []byte("hi"))
	tpl.Execute(os.Stdout, []any{1, "a", int8(2), []int{3}})
	tpl.Execute(os.Stdout, [2]int{1, 2})
	tpl.Execute(os.Stdout, [][]string{{"a"}, {"b", "c"}})
	tpl.Execute(os.Stdout, map[string]int{"b": 2, "a": 1})
	tpl.Execute(os.Stdout, map[string]any{"a": 1, "b": []int{2}})
	tpl.Execute(os.Stdout, map[int8][]bool{1: {true}})
	tpl.Execute(os.Stdout, map[time.Month]int{time.March: 3, time.January: 1})
	tpl.Execute(os.Stdout, []time.Duration{time.Second, 5})
	tpl.Execute(os.Stdout, []time.Time{time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)})

	ops := template.Must(template.New("o").Parse(
		"{{len .S}} {{index .S 1}} {{index .M \"a\"}} {{range $k, $v := .M}}{{$k}}={{$v}} {{end}}{{eq (index .S 0) 1}} {{lt .F 1.0}} {{printf \"%T\" .F}}\n"))
	ops.Execute(os.Stdout, map[string]any{"S": []int{1, 2}, "M": map[string]int8{"a": 1, "b": 2}, "F": float32(0.5)})
}
