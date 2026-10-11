// Package desc is a function-hook plugin: its Config() installs
// DescField and DescDecl under the "descField"/"descDecl" keys, which
// the walk calls — inside the same VM — for every description it
// emits. No overrides.
//
// The hook parameters stay `any` on the boundary (a static assertion
// naming *inspect.Field would compare the func type nominally across
// package instances and trap); the hook reads the field reference
// through reflect instead — names, not type identity.
package desc

import (
	"reflect"
	"strings"

	"github.com/podhmo/minigo/examples/extract-schema/extschema"
)

// Configure is the plugin's single entry point: the walk calls it per
// plugin dir with the shared registrar.
func Configure(c *extschema.Config) {
	c.DescField(DescField)
	c.DescDecl(DescDecl)
}

// fieldNames reads (*inspect.Field).Names off the field reference.
func fieldNames(f any) []string {
	rv := reflect.ValueOf(f)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	ns := rv.FieldByName("Names")
	out := []string{}
	for i := 0; i < ns.Len(); i++ {
		out = append(out, ns.Index(i).String())
	}
	return out
}

// DescField drops the Go doc convention's leading "<FieldName>" (or
// "<FieldName>:") from a field description: "// Lead is ..." ->
// "is ...". The field reference lets a filter read names, tags, or
// position instead of guessing at the text.
func DescField(f any, doc string) string {
	for _, n := range fieldNames(f) {
		if strings.HasPrefix(doc, n) {
			s := strings.TrimSpace(doc[len(n):])
			s = strings.TrimPrefix(s, ":")
			return strings.TrimSpace(s)
		}
	}
	return doc
}

// DescDecl drops the leading "<TypeName>" from a type description:
// "// Commented covers ..." -> "covers ...". The reference is the
// *inspect.Decl view; here we only need its Name field.
func DescDecl(v any, doc string) string {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	name := rv.FieldByName("Name").String()
	if strings.HasPrefix(doc, name) {
		s := strings.TrimSpace(doc[len(name):])
		s = strings.TrimPrefix(s, ":")
		return strings.TrimSpace(s)
	}
	return doc
}
