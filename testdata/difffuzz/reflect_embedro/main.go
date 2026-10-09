package main

import (
	"fmt"
	"reflect"
)

// exported fields reached through an unexported EMBEDDED type stay
// settable (reflect's flagEmbedRO); through an unexported named field
// they do not (flagStickyRO). yaml.v3 sets `yaml:",inline"` fields of
// an unexported embed this way.

type local struct {
	Name string
	low  int
}

type outer struct {
	local
	hidden local
	Out    string
}

// pointer/slice embeds exercise the same flag through Elem and Index:
// pointer Elem preserves flagEmbedRO (an exported field below stays
// settable), while Index upgrades it to sticky like gc's v.flag.ro().

type outerPtr struct {
	*local
}

type locals []local

type outerSlice struct {
	locals
}

func main() {
	var o outer
	v := reflect.ValueOf(&o).Elem()
	emb := v.Field(0)
	fmt.Println("embed:", emb.CanSet(), emb.CanInterface())
	n := emb.Field(0)
	fmt.Println("embed.Name:", n.CanSet(), n.CanInterface())
	n.SetString("set")
	fmt.Println("embed.low:", emb.Field(1).CanSet())
	h := v.Field(1)
	fmt.Println("hidden.Name:", h.Field(0).CanSet(), h.Field(0).CanInterface())
	fmt.Println(o.Name)

	op := outerPtr{local: &local{Name: "ptr"}}
	e := reflect.ValueOf(&op).Elem().Field(0).Elem()
	fmt.Println("ptr-embed Elem:", e.CanSet(), e.CanInterface())
	ex := e.Field(0)
	fmt.Println("ptr-embed Elem.Name:", ex.CanSet(), ex.CanInterface())
	ex.SetString("set via elem")
	fmt.Println(op.local.Name)

	os := outerSlice{locals{{Name: "idx"}}}
	i := reflect.ValueOf(&os).Elem().Field(0).Index(0)
	fmt.Println("slice-embed Index:", i.CanSet(), i.CanInterface())
	fmt.Println("slice-embed Index.Name:", i.Field(0).CanSet(), i.Field(0).CanInterface())
}
