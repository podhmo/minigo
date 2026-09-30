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

type hidden struct{ x int }

var hiddenVar = 1

func main() {}
