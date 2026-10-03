// Package inspectpkg is the introspection subject for inspect tests.
package inspectpkg

import (
	"strings"
	"time"
)

// Base is embedded into User.
type Base struct {
	ID int64
}

// User is a struct type to walk. Its doc comment is introspectable.
type User struct {
	// Name is the user name.
	Name string `json:"name"`
	Age  int
	*Base
	Builder strings.Builder
}

// Greet says hello.
func (u *User) Greet() string { return "hi " + u.Name }

// Bye says goodbye — a second method.
func (u User) Bye() string { return "bye " + u.Name }

// Hello is a plain function.
func Hello(s string) string { return "hello " + s }

// Count is a package var.
var Count int = 3

// Label is a package const.
const Label = "lbl"

// MyInt is a newtype over int.
type MyInt int

// PInt is a newtype over *MyInt (pointer under a declared layer).
type PInt *MyInt

// AInt aliases int.
type AInt = int

// Dur aliases a foreign type.
type Dur = time.Duration

type (
	// AFloat aliases a basic type inside a grouped decl.
	AFloat = float64
	// BFloat is a defined type in the same group — per-spec Assign.
	BFloat float64
)

// Tricky is a defined type whose line carries a '=' — only in a comment.
type Tricky int // = not an alias

// AliasEnum aliases int; constants can still be typed with it —
// enum-ness is orthogonal to alias-ness.
type AliasEnum = int

// AE1 is an enum member of an alias type.
const AE1 AliasEnum = 1

// APair is a generic alias.
type APair[T any] = Pair[T]

// StrList is a named slice type.
type StrList []string

// Node is a recursive pointer type — Origin must terminate on it.
type Node *Node

// Arr2 and Arr3 differ only in array length.
type Arr2 [2]int

type Arr3 [3]int

// ArrExpr spells the same length as Arr2 differently.
type ArrExpr [1 + 1]int

const sizeN = 2

// ArrN uses a named constant for the same length.
type ArrN [sizeN]int

const sizeM = 4

// ArrM uses a named constant for a different length.
type ArrM [sizeM]int

// Speaker is a small interface with one named requirement.
type Speaker interface {
	Speak() string
}

// Status is an int enum type.
type Status int

const (
	StatusUnknown Status = iota
	StatusTodo
	StatusDone
)

// StatusExtra is a member in a separate const decl.
const StatusExtra Status = 99

const (
	FlagA Status = iota
	FlagB        // inherits the Status type
	FlagC = 100  // untyped spec — breaks the inheritance chain
	FlagD        // inherits the untyped spec: NOT a member
)

// Priority is a string enum type with a multi-name spec.
type Priority string

const (
	Low    Priority = "low"
	High   Priority = "high"
	PA, PB Priority = "pa", "pb"
)

// Loose is an untyped constant — never an enum member.
const Loose = 42

// ForDur is a const typed by a foreign package — never an enum member.
const ForDur time.Duration = time.Second

// CurrentStatus is a var of the enum type — not an enum member.
var CurrentStatus Status = StatusTodo

// Talker embeds Speaker and adds a named method spec.
type Talker interface {
	Speaker
	Talk(msg string) error
}

// Number is a constraint interface — type elements only.
type Number interface {
	~int | ~int64
}

// Rec exercises composite field shapes: map, chan, func, interface,
// a named-type field, and a generic instantiation.
type Rec struct {
	Table map[string]int
	Out   chan string
	Fn    func(int) bool
	If    Speaker
	M     MyInt
	IP    Pair[int]
}

// Pair is a generic type for TypeParams coverage.
type Pair[T any] struct {
	A T
	B T
}

// Reduce is a generic function for TypeParams coverage.
func Reduce[T Number](xs []T, init T) T { return init }

// Cage is a second generic — SameType must not collapse distinct
// instantiations that share arguments.
type Cage[T any] struct {
	V T
}

// Two is a two-parameter generic for IndexListExpr coverage.
type Two[K, V any] struct {
	First  K
	Second V
}

// Inst holds instantiations of different generics.
type Inst struct {
	P Pair[int]
	C Cage[int]
	D Two[int, string]
}

// I0 is an embed target for Anon's interface field.
type I0 interface {
	Zero()
}

// Anon carries anonymous composite fields for TypeFields coverage.
type Anon struct {
	F struct {
		W string `json:"w"`
		N int
	}
	G interface {
		M(int) string
		I0
	}
	S []struct {
		X int `json:"x"`
	}
}

type hidden struct{ x int }

var hiddenVar = 1

// Path deliberately collides with the *runtime.Package.Path metadata
// field — member access resolves the package namespace first.
var Path = "member-shadow"

func main() {}
