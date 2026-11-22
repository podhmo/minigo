// Package inferpkg exercises untyped `var x = <expr>` decls whose type
// the REPL completer must infer without running init.
package inferpkg

import (
	"github.com/podhmo/minigo/testdata/infercallee"
	"github.com/podhmo/minigo/testdata/inspectpkg"
	. "github.com/podhmo/minigo/testdata/inferdot"
)

func NewUser() inspectpkg.User { return inspectpkg.User{Name: "new"} }

func Pair() (inspectpkg.User, inspectpkg.Base) {
	return inspectpkg.User{}, inspectpkg.Base{}
}

// Untyped vars: the completer derives each type from the initializer.
var (
	CallUser  = NewUser()            // same-package call
	CrossT    = infercallee.New()    // pkg.F() through the file's import
	DotT      = NewDot()             // dot-imported callee
	LitUser   = inspectpkg.User{}    // composite literal
	NewPtr    = new(inspectpkg.User) // new(T) → pointer typedef
	AliasVar  = CallUser             // var-to-var chain
	ElemUser  = userMap["k"]         // index into a typed map
	SelField  = holderVar.U          // field via a declared type
	DerefUser = *ptrUser             // deref of a declared pointer
	A, B      = Pair()               // result splitting: A User, B Base
)

var (
	userMap   = map[string]inspectpkg.User{"k": {Name: "k"}}
	holderVar = Holder{U: inspectpkg.User{Name: "h"}}
	ptrUser   = &inspectpkg.User{}
)

type Holder struct {
	U inspectpkg.User
}
