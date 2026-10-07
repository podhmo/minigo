package main

import (
	"bytes"
	"fmt"
	"reflect"
	"text/template"
)

// MethodByName must not return a struct FIELD of that name — text/template
// probes MethodByName before FieldByName, so {{.FieldName}} on a struct
// with a FieldName field otherwise tries to call a string.

type Info struct {
	FieldName  string
	IsOptional bool
}

func (i Info) Upper() string { return "U:" + i.FieldName }

func main() {
	v := reflect.ValueOf(Info{FieldName: "probe"})
	p := reflect.ValueOf(&Info{FieldName: "p"})
	fmt.Println(v.MethodByName("FieldName").IsValid(), p.MethodByName("FieldName").IsValid())
	fmt.Println(v.MethodByName("Upper").IsValid(), p.MethodByName("Upper").IsValid())
	t := template.Must(template.New("t").Parse(`{{.FieldName}}{{if .IsOptional}},omitempty{{end}} {{.Upper}}`))
	var b bytes.Buffer
	fmt.Println(t.Execute(&b, Info{FieldName: "probe", IsOptional: true}), b.String())
}
