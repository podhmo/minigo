package app

import (
	"time"

	"github.com/podhmo/minigo/examples/gen-sync/app/internal/meta"
	"github.com/podhmo/minigo/examples/gen-sync/app/internal/mood"
)

// Outer reaches a required-bearing struct through a pointer field.
type Outer struct {
	In *Inner
}

// Inner carries the required tag the containers' directives propagate
// from. It earns one on its own surface too.
type Inner struct {
	V string `required:"true"`
}

// NodeA and NodeB reference each other — the reach must terminate on
// the cycle, and both earn a directive because NodeB is required.
type NodeA struct {
	B *NodeB
}

type NodeB struct {
	A    *NodeA
	Name string `required:"true"`
}

// Orbit and Moon form a cycle with nothing required inside — the reach
// still has to terminate, and no directive is earned.
type Orbit struct {
	M *Moon
}

type Moon struct {
	O *Orbit
}

// Loop points at itself — self-reference with no required tag anywhere.
type Loop struct {
	Next *Loop
}

// Bag reaches Inner through a slice, a map value, and a direct field.
type Bag struct {
	Items []Inner
	ByID  map[string]*Inner
	In    Inner
}

// Alias is an alias chain hop: fields of type Alias reach Inner through
// the named decl's own definition.
type Alias = Inner

// ViaAlias reaches Inner through one alias indirection.
type ViaAlias struct {
	A Alias
}

// Holder reaches Inner through a generic instantiation's base and
// argument — both are children the view exposes.
type Holder struct {
	L List[Inner]
}

// List is a generic container whose body never resolves anywhere (V T
// names a type parameter, which is not a decl).
type List[T any] struct {
	V T
}

// Safe reaches Vault only through the instantiation's base — its
// argument (int) leads nowhere.
type Safe struct {
	V Vault[int]
}

// Vault is a generic container carrying the required tag; it is
// reachable only through an instantiation's base.
type Vault[T any] struct {
	Item T `required:"true"`
}

// Inline hides a required tag inside an anonymous struct field — the
// tag is readable through TypeFields, so this earns the directive.
type Inline struct {
	F struct {
		W string `required:"true"`
	}
}

// ListAnon holds a slice of an anonymous struct whose field carries
// the tag — composites that compose a struct count too.
type ListAnon struct {
	Items []struct {
		X int `required:"true"`
	}
}

// Shadow holds same-named types from another package — meta.Inner has
// no required fields; only the resolved package decides, not the bare
// name. time.Time leaves the subtree and is never resolved at all.
type Shadow struct {
	M meta.Inner
	P *meta.Plain
	T time.Time
}

// Remote reaches across the package boundary inside the subtree:
// mood.Marked carries a required field, so Remote earns the directive
// even when mood itself is not a sync target (exploration scope is the
// subtree, sync scope is the visited packages).
type Remote struct {
	M *mood.Marked
}
