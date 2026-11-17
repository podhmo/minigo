package main

import (
	"fmt"
	"maps"

	hm "hostmaps.test/hm"
)

func InsertRoundTrip() string {
	maps.Insert(hm.DstStrs, maps.All(hm.SrcStrs))
	maps.Insert(hm.DstAny, maps.All(hm.SrcAny))
	return fmt.Sprint(hm.DstStrs["a"], hm.DstAny["k"])
}

func CollectValues() []any {
	var out []any
	for v := range maps.Values(hm.NanMap) {
		out = append(out, v)
	}
	return out
}
