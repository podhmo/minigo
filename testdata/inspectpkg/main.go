// Package inspectpkg is the introspection subject for inspect tests.
package inspectpkg

import "strings"

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

type hidden struct{ x int }

var hiddenVar = 1

// Path deliberately collides with the *runtime.Package.Path metadata
// field — member access resolves the package namespace first.
var Path = "member-shadow"

func main() {}
