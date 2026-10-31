package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

type P struct {
	Name string `json:"name"`
	Age  int    `json:"age,omitempty"`
}

type Key int

func (k Key) MarshalText() ([]byte, error) { return []byte(fmt.Sprintf("key%d", int(k))), nil }

type Shout string

func (s Shout) MarshalJSON() ([]byte, error) { return []byte(`"!` + string(s) + `!"`), nil }

type upper struct{ buf bytes.Buffer }

func (u *upper) Write(p []byte) (int, error) { return u.buf.Write(bytes.ToUpper(p)) }

func enc(label string, v any) {
	var b bytes.Buffer
	err := json.NewEncoder(&b).Encode(v)
	m, merr := json.Marshal(v)
	fmt.Printf("%s: %s err=%v marshal=%s err=%v\n", label, bytes.TrimSpace(b.Bytes()), err, m, merr)
}

func main() {
	enc("struct", P{Name: "a"})
	enc("typed-slice-of-maps", []map[string]any{{"k": P{Name: "x"}}})
	enc("slice-of-slices", [][]any{{P{Name: "y"}}})
	enc("map-of-slices", map[string][]any{"s": {P{Name: "z", Age: 3}}})
	enc("int-keyed", map[int]any{1: P{Name: "w"}})
	enc("marshaltext-keys", map[Key]int{2: 1, 1: 2})
	enc("marshaljson-value", map[string]Shout{"s": "hi"})
	x := 5
	enc("empty-key-ptr", map[string]any{"": &x, "a": 1})
	p := &P{Name: "p"}
	enc("field-ptr", map[string]any{"": &p.Name, "a": 1})
	var arr [2]int
	arr[1] = 7
	enc("idx-ptr", map[string]any{"": &arr[1], "a": 1})

	rows := []map[string]any{{"a": 1}}
	json.NewEncoder(&bytes.Buffer{}).Encode(rows)
	rows[0]["a"] = 2
	fmt.Println("caller data intact:", rows[0]["a"])

	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	encode := e.Encode
	encode(map[string]any{"indent": []P{{Name: "i"}}})

	u := &upper{}
	json.NewEncoder(u).Encode(P{Name: "script writer"})
	fmt.Print(u.buf.String())
}
