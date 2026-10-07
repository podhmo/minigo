package main

import (
	"fmt"
	"reflect"
	"strings"
)

// string(field.Tag) converts a host reflect.StructTag — yaml.v3's
// getStructInfo checks `strings.Index(string(field.Tag), ":")`.

type T struct {
	A string `yaml:",inline"`
	B string `json:"b"`
	C string
}

func main() {
	st := reflect.TypeOf(T{})
	for i := 0; i < st.NumField(); i++ {
		field := st.Field(i)
		fmt.Printf("%s %q %d\n", field.Name, string(field.Tag), strings.Index(string(field.Tag), ":"))
	}
}
