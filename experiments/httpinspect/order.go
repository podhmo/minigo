package httpinspect

import (
	"sort"
	"strings"

	"github.com/podhmo/minigo/inspect"
)

// A plain variable read is not necessarily sequenced before a neighboring
// function call. Re-read only expressions with no calls or allocation; never
// execute an argument twice merely to explore another evaluation order.
func canReRead(n *inspect.Node) bool {
	if n == nil {
		return false
	}
	switch n.Kind {
	case "Ident", "BasicLit":
		return true
	case "ParenExpr", "StarExpr":
		return canReRead(child(n, "X"))
	case "SelectorExpr":
		return canReRead(child(n, "X"))
	default:
		return false
	}
}
func semanticKey(v value, store map[string]value) string {
	var parts []string
	for _, x := range v {
		s := x.kind + ":" + x.text + "@" + x.origin
		if x.object != nil {
			obj := x.object
			s += "/" + obj.typ.Package.Path + "." + obj.typ.Name
			if obj.pointer && obj.binding != "" {
				s += "/address:" + obj.binding
			} else {
				names := make([]string, 0, len(obj.fields))
				for name := range obj.fields {
					names = append(names, name)
				}
				sort.Strings(names)
				for _, name := range names {
					id := obj.fields[name]
					if obj.pointer {
						s += "/" + name + ":" + id
					} else {
						s += "/" + name + ":" + semanticKey(store[id], store)
					}
				}
			}
		}
		if x.fn != nil {
			fn := x.fn
			s += "/fn:" + fn.name + "/receiver:" + semanticKey(fn.receiver, store)
			names := make([]string, 0, len(fn.captures))
			for name := range fn.captures {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				s += "/capture:" + name + ":" + fn.captures[name]
			}
		}
		parts = append(parts, s)
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}
func mergeSlots(a, b []string) []string {
	out := append([]string(nil), a...)
	for _, id := range b {
		found := false
		for _, old := range out {
			if old == id {
				found = true
			}
		}
		if !found {
			out = append(out, id)
		}
	}
	return out
}
func sameSlots(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i, id := range a {
		if b[i] != id {
			return false
		}
	}
	return true
}
