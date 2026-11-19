// Package litkeys is the subject for composite-literal key
// classification: a key refers to a declaration only in a map or array
// literal, never in a struct literal. Struct fields here are named KeyA
// on purpose, so a field key misread as an expression would resolve to
// the KeyA const instead of reporting no SymbolID.
package litkeys

import (
	"net/http"
	"net/url"
)

const (
	KeyA = "a"
	KeyB = "b"
	Idx  = 0
)

type Lookup map[string]int

type Pair[K comparable, V any] map[K]V

type Rows []map[string]int

type MA = map[string]int

type MB = Lookup

type Shadow map[string]int

type St struct{ KeyA int }

type Sts []St

type Box[T any] struct{ KeyA T }

// GenericMap: a generic named map takes its shape from the generic decl.
func GenericMap() int {
	m := Pair[string, int]{KeyA: 1}
	return len(m)
}

// ElidedInner: elided inner literals take the outer element type.
func ElidedInner() int {
	a := []map[string]int{{KeyA: 1}}
	b := map[string]map[string]int{KeyA: {KeyB: 2}}
	c := []*map[string]int{{KeyA: 3}}
	d := Rows{{KeyA: 4}}
	e := map[[2]string]int{{Idx: KeyA}: 5}
	return len(a) + len(b) + len(c) + len(d) + len(e)
}

// LocalNamed: local named maps and aliases resolve through the local
// type decl in scope.
func LocalNamed() int {
	type L map[string]int
	type LA = L
	l := L{KeyA: 1}
	la := LA{KeyA: 2}
	ls := []LA{{KeyA: 3}}
	return len(l) + len(la) + len(ls)
}

// Aliases: package-level aliases of a map type and of a named map.
func Aliases() int {
	a := MA{KeyA: 1}
	b := MB{KeyA: 2}
	return len(a) + len(b)
}

// Imported: an imported type resolves when the imported package is
// already materialized — the test materializes net/url first.
func Imported() int {
	v := url.Values{KeyA: nil}
	return len(v)
}

// ImportedCold: an imported type whose package is not materialized
// stays unresolved — inspect never loads a package for this.
func ImportedCold() int {
	h := http.Header{KeyA: nil}
	return len(h)
}

// GenericElem: an element whose type is a type parameter resolves
// through the instantiation's argument at the call site.
func GenericElem() int {
	m := Pair[string, map[string]int]{KeyA: {KeyB: 1}}
	return len(m)
}

// Structs: struct keys are field names, elided or not.
func Structs() int {
	a := St{KeyA: 1}
	b := []St{{KeyA: 2}}
	c := []*St{{KeyA: 3}}
	d := map[string]St{KeyB: {KeyA: 4}}
	e := Sts{{KeyA: 5}}
	f := Box[int]{KeyA: 6}
	g := map[St]int{{KeyA: 7}: 1}
	return a.KeyA + len(b) + len(c) + len(d) + len(e) + f.KeyA + len(g)
}

// LocalShadow: a local struct type shadows the package-level map of the
// same name; its keys are field names.
func LocalShadow() int {
	type Shadow struct{ KeyA int }
	s := Shadow{KeyA: 1}
	return s.KeyA
}

// SiblingScopes: same-named local types in sibling blocks are told apart
// by scope.
func SiblingScopes() int {
	n := 0
	{
		type S map[string]int
		n += len(S{KeyA: 1})
	}
	{
		type S struct{ KeyA int }
		n += S{KeyA: 2}.KeyA
	}
	return n
}

// SiblingVarShadow: a var bound in the first block no longer hides the
// package-level map type used in the sibling block — the scoped local
// check still treats an in-scope local type decl as shadowing.
func SiblingVarShadow() int {
	n := 0
	{
		Lookup := 9
		n += Lookup
	}
	{
		n += len(Lookup{KeyA: 1})
	}
	return n
}

// LocalElidedStruct: an elided struct element of a local named slice.
func LocalElidedStruct() int {
	type LS []struct{ KeyA int }
	return len(LS{{KeyA: 1}})
}

type StB struct{ KeyA int }

// SpecArgShadow: the alias's type argument is spelled inside the
// package-level spec — a same-named local type in this func must not
// reach into the spec's resolution (the func precedes the spec so the
// local's position would shadow a scope-oblivious lookup).
func SpecArgShadow() {
	type StB map[string]int
	_ = SpecArgAlias{K: {KeyA: 1}}
}

type SpecArgAlias = Pair[string, StB]

// AliasLocalShadow: the alias resolves url.Values at package scope;
// this func's local `url` type must not refuse the foreign lookup.
func AliasLocalShadow() {
	type url map[string]int
	_ = AliasURL{KeyA: nil}
}

type AliasURL = url.Values
