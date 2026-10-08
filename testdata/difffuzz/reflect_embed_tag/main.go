package main

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// an embedded field keeps its struct tag — yaml.v3 reads `yaml:",inline"`
// off oapi-codegen's `codegen.Configuration` embed.

type Inner struct {
	Name string `json:"name"`
}

type Outer struct {
	Inner `yaml:",inline" json:"inner"`
	*Ptr  `json:"-"`
	Out   string `json:"out"`
}

type Ptr struct{ P int }

func main() {
	type Local struct {
		Inner `yaml:",inline"`
	}
	for _, t := range []reflect.Type{reflect.TypeOf(Outer{}), reflect.TypeOf(Local{})} {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			fmt.Printf("%s anon=%v yaml=%q json=%q\n", f.Name, f.Anonymous, f.Tag.Get("yaml"), f.Tag.Get("json"))
		}
	}
	b, err := json.Marshal(Outer{Inner: Inner{Name: "n"}, Out: "o"})
	fmt.Println(string(b), err)
}
