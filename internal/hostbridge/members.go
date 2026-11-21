// members.go indexes a host value's field/method surface the way
// runtime.TypeByName lookups do — field index, method index, and the
// promoted search that follows Go's embedding rules.
package hostbridge

import (
	"reflect"
)

// hostMemberInner reports how deeply name is promoted inside the boxed
// type — 0 for a direct exported field or a method of the slot type,
// higher when it resolves through the host's own embeds
// (*template.Template → *parse.Tree → Root is inner 1), -1 when absent.
// Answered at type level so a zero whose anonymous pointer fields are
// nil still resolves; only actual member access dereferences the stored
// value. methods is false for a defined type over a host type: it
// carries fields, not the method set.
func HostMemberInner(zero any, name string, methods bool) int {
	t := reflect.TypeOf(zero)
	if t == nil {
		return -1
	}
	st := t
	for st.Kind() == reflect.Pointer {
		st = st.Elem()
	}
	if st.Kind() == reflect.Struct {
		if sf, ok := st.FieldByName(name); ok && sf.PkgPath == "" {
			return len(sf.Index) - 1 // 0 = a direct field
		}
	}
	if methods {
		if _, ok := t.MethodByName(name); ok {
			return HostMethodInner(t, name)
		}
	}
	return -1
}

// HostPromotedMethodInner reports the embed depth inside the boxed
// type at which name's method is promoted through anonymous fields —
// the members a defined type over the host type keeps: gc promotes
// Inner.M for `type Wrap Base` where Base = struct{ Inner }, but none
// of Base's own declared methods. -1 when name does not reach the
// slot type through an anonymous field's method set.
func HostPromotedMethodInner(zero any, name string) int {
	t := reflect.TypeOf(zero)
	if t == nil {
		return -1
	}
	st := t
	for st.Kind() == reflect.Pointer {
		st = st.Elem()
	}
	if st.Kind() != reflect.Struct {
		return -1
	}
	underPtr := t.Kind() == reflect.Pointer
	best := -1
	for i := 0; i < st.NumField(); i++ {
		sf := st.Field(i)
		if !sf.Anonymous {
			continue
		}
		// methods promote through an unexported anonymous field too —
		// only the field itself is invisible outside its package.
		ct := sf.Type
		if ct.Kind() != reflect.Pointer && ct.Kind() != reflect.Interface && underPtr {
			ct = reflect.PointerTo(ct)
		}
		if _, ok := ct.MethodByName(name); !ok {
			continue
		}
		if d := MethodInner(ct, name, map[reflect.Type]bool{}); d >= 0 && (best < 0 || d+1 < best) {
			best = d + 1
		}
	}
	return best
}

// hostMethodInner counts the embed levels inside the host type that
// name's method is promoted through — 0 when it is declared on t's own
// method set. t is the slot's declared type: a *T slot contributes the
// methods of *T, including pointer receivers of embedded values.
func HostMethodInner(t reflect.Type, name string) int {
	return MethodInner(t, name, map[reflect.Type]bool{})
}

// methodInner: name is in t's method set — how deeply inside t it is
// promoted. Each anonymous field contributes its own type's method set
// — *E when the chain went through a pointer, an interface as itself —
// and the shallowest path wins. reflect cannot tell a method declared
// on t from one promoted through an embed, so a name found in a
// child's set counts via that child; contributed types are visited
// once per path so a recursively-embedded host type (struct{ *T })
// terminates instead of overflowing the stack.
func MethodInner(t reflect.Type, name string, seen map[reflect.Type]bool) int {
	st := t
	for st.Kind() == reflect.Pointer {
		st = st.Elem()
	}
	if st.Kind() != reflect.Struct {
		return 0 // non-struct types declare their methods
	}
	if seen[t] {
		return -1
	}
	seen[t] = true
	defer delete(seen, t)
	underPtr := t.Kind() == reflect.Pointer
	best := -1
	for i := 0; i < st.NumField(); i++ {
		sf := st.Field(i)
		if !sf.Anonymous {
			continue
		}
		ct := sf.Type
		if ct.Kind() != reflect.Pointer && ct.Kind() != reflect.Interface && underPtr {
			// a value embed contributes *E's method set when the
			// embedding chain went through a pointer.
			ct = reflect.PointerTo(ct)
		}
		if _, ok := ct.MethodByName(name); !ok {
			continue
		}
		if d := MethodInner(ct, name, seen); d >= 0 && (best < 0 || d+1 < best) {
			best = d + 1
		}
	}
	if best >= 0 {
		return best
	}
	return 0
}
