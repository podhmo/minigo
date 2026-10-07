package main

// reflect.Type of an intrinsic-bound func (strings.ToLower) reports the
// real signature — text/template.Funcs validates FuncMap entries by it.

import (
	"fmt"
	"reflect"
	"strings"
)

type caser struct{ p string }

func (c caser) String(s string) string { return c.p + s }

func dict(values ...any) (map[string]any, error) { return nil, nil }

func main() {
	c := caser{"T:"}
	for _, f := range []any{strings.ToLower, c.String, dict, strings.Repeat} {
		t := reflect.ValueOf(f).Type()
		fmt.Println(t.Kind(), t.NumIn(), t.NumOut(), t.IsVariadic())
	}
}
