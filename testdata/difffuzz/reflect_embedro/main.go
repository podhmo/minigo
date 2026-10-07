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
}
