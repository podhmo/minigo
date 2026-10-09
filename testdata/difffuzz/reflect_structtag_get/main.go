package main

import (
	"fmt"
	"reflect"
	"strings"
)

// reflect.StructTag on a script string carries Get/Lookup: tag-driven
// sync scripts call them on inspect-style tag strings, e.g.
// reflect.StructTag(f.Tag).Get("json").

type T struct {
	Name string `json:"name,omitempty" db:"name"`
	Skip string `json:"-"`
}

type getter interface {
	Get(string) string
}

func main() {
	st := reflect.TypeOf(T{})
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		fmt.Println(f.Name, f.Tag.Get("json"))
	}

	f := st.Field(0)
	tag := reflect.StructTag(string(f.Tag))
	fmt.Println(tag.Get("json"))
	if v, ok := tag.Lookup("db"); ok {
		fmt.Println(v, ok)
	}
	fmt.Println(strings.Split(tag.Get("json"), ",")[0])

	// a named string keeps its string ops, and the facade's Tag field
	// (a host-boxed StructTag) compares by its string.
	fmt.Println(tag == `json:"name,omitempty" db:"name"`, len(tag), tag[0])
	fmt.Println(tag == f.Tag, st.Field(1).Tag == "")
	for _, r := range tag {
		fmt.Print(r, " ")
	}
	fmt.Println()
	fmt.Println(len([]byte(tag)))

	var zero reflect.StructTag
	fmt.Println(zero == "", zero.Get("json") == "")

	var g getter = tag
	fmt.Println(g.Get("db"))

	// method expressions dispatch on the receiver argument
	fmt.Println(reflect.StructTag.Get(tag, "db"))
	if v, ok := reflect.StructTag.Lookup(tag, "json"); ok {
		fmt.Println(v)
	}

	// facade + parsing edges: FieldByName's (StructField, bool), Lookup
	// miss, values containing spaces, malformed pairs gc's own parser
	// drops.
	sf, ok := st.FieldByName("Name")
	fmt.Println(ok, sf.Tag.Get("json"))
	if _, ok := st.FieldByName("Nope"); !ok {
		fmt.Println("no Nope")
	}
	multi := reflect.StructTag(`a:"1" b:"2" c:"x y"`)
	fmt.Println(multi.Get("b"), multi.Get("a"), multi.Get("z"))
	lv, lok := multi.Lookup("c")
	fmt.Println(lv, lok)
	fmt.Println(reflect.StructTag(`bad"tag`).Get("bad"), reflect.StructTag(`x:"1" bad`).Get("x"))
	bad := reflect.StructTag(`name:"x" bad name2:"y"`)
	fmt.Println(bad.Get("name"), bad.Get("bad"), bad.Get("name2"))
}
