package main

import (
	"fmt"
	"reflect"
)

// reflect.StructTag(x) conversion produces the bound host type; gc's
// Get/Lookup tag parsing must work on it (space-separated key:"value"
// pairs, malformed pairs dropped), same as on Field.Tag.

type S struct {
	Name string `json:"name,omitempty"`
	Low  int    `json:"-"`
}

func main() {
	t := reflect.TypeOf(S{})
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		fmt.Println(f.Name, f.Tag.Get("json"))
	}
	f, ok := t.FieldByName("Name")
	v, ok2 := f.Tag.Lookup("json")
	v2, ok3 := f.Tag.Lookup("missing")
	fmt.Println(ok, v, ok2, v2, ok3)

	tag := reflect.StructTag(`a:"1" b:"2" c:"x y"`)
	fmt.Println(tag.Get("b"), tag.Get("a"), tag.Get("z"))
	lv, lok := tag.Lookup("c")
	fmt.Println(lv, lok)

	bad := reflect.StructTag(`name:"x" bad name2:"y"`)
	fmt.Println(bad.Get("name"), bad.Get("bad"), bad.Get("name2"))

	var zero reflect.StructTag
	fmt.Println(zero.Get("a"))
}
