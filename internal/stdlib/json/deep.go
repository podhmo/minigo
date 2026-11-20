package stdjson

import (
	"encoding/json"
	"fmt"
)

// jsonDomDeep unwraps the DOM into the plain tree `any` targets keep:
// members collapse last-wins into a map like gc's untyped decode, and
// json.Number stays only when the caller asked for UseNumber. Without
// UseNumber a literal float64 cannot hold (1e309) fails the decode
// like gc's literalStore, not silently stringifies.
func jsonDomDeep(n *jsonDom, ectx *jsonErrCtx) any {
	if n == nil {
		return nil
	}
	switch x := n.val.(type) {
	case []jsonMember:
		out := make(map[any]any, len(x))
		for _, m := range x {
			out[m.key] = jsonDomDeep(m.node, ectx)
		}
		return out
	case []*jsonDom:
		out := make([]any, len(x))
		for i, c := range x {
			out[i] = jsonDomDeep(c, ectx)
		}
		return out
	case json.Number:
		if ectx.keepNum {
			return x
		}
		f, err := x.Float64()
		if err != nil {
			if ectx != nil {
				ectx.failErr(fmt.Errorf("json: cannot unmarshal number %s into Go value of type float64", x.String()))
			}
			// gc still stores the strconv result (±Inf on range
			// errors) alongside the recorded error.
			return f
		}
		return f
	default:
		return x
	}
}
