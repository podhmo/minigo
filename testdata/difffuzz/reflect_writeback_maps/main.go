package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/template"
)

type M map[string]any

func main() {
	rows := []map[string]any{{"a": 1, "b": "x"}, {"a": 2}}
	t := template.Must(template.New("t").Parse("{{range .}}{{.a}};{{end}}\n"))
	t.Execute(os.Stdout, rows)
	rows[0]["z"] = 2
	rows[1]["c"] = 3
	fmt.Println(len(rows[0]), rows[0]["a"], rows[0]["z"], len(rows[1]), rows[1]["c"])

	arr := [2]map[string]int{{"k": 1}, {"k": 2}}
	t2 := template.Must(template.New("t2").Parse("{{range .}}{{.k}}{{end}}\n"))
	t2.Execute(os.Stdout, arr)
	arr[0]["k"] = 10
	fmt.Println(arr[0]["k"], arr[1]["k"])

	named := []M{{"n": 1}}
	t3 := template.Must(template.New("t3").Parse("{{range .}}{{.n}}{{end}}\n"))
	t3.Execute(os.Stdout, named)
	named[0]["n"] = 5
	fmt.Println(named[0]["n"])

	nested := []map[string][]int{{"xs": {1, 2}}}
	t4 := template.Must(template.New("t4").Parse("{{range .}}{{index .xs 1}}{{end}}\n"))
	t4.Execute(os.Stdout, nested)
	nested[0]["xs"] = append(nested[0]["xs"], 3)
	fmt.Println(nested[0]["xs"])

	var b bytes.Buffer
	t.Execute(&b, rows)
	fmt.Println(strings.TrimSpace(b.String()))

	// a reflected host call that does not touch maps still writes
	// scalars back (io.ReadFull fills the byte slice).
	buf := make([]byte, 3)
	io.ReadFull(strings.NewReader("xyz"), buf)
	fmt.Println(string(buf))
	json.NewEncoder(os.Stdout).Encode(rows)
}
