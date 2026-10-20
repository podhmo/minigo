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

// TwoArgs groups two names in one field and ends variadic — the arity
// a field-group count cannot see.
func TwoArgs(a, b int, c ...string) {}

// PosArgs takes unnamed params only.
func PosArgs(int, string) {}

// Divmod groups two names in one result field.
func Divmod(a, b int) (q, r int) { return 0, 0 }

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

// Span exercises enum members whose type is only inferred from the
// initializer, plus an unexported member.
type Span int64

const (
	SpanMs     Span = 1
	SpanSecond      = 1000 * SpanMs // inferred-type member
	spanMin    Span = -1            // unexported sentinel
)

// Widget exercises a composite "const" — tolerated by the interpreter
// but not a Go constant expression, so never an enum member.
type Widget struct{ X int }

const DefaultWidget Widget = Widget{X: 1}

// Loose is an untyped constant — never an enum member.
const Loose = 42

// ForDur is a const typed by a foreign package — never an enum member.
const ForDur time.Duration = time.Second

// CurrentStatus is a var of the enum type — not an enum member.
var CurrentStatus Status = StatusTodo

// Bare declares no initializer.
var Bare int

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

// Head is a generic method — its T binds through the receiver's
// instantiation, not a method-local type parameter list.
func (p Pair[T]) Head() T { return p.A }

// List is a generic over a composite underlying — its element type
// mentions the parameter.
type List[T any] []T

// IPair is a non-generic alias to a generic instantiation.
type IPair = Pair[int]

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

// Greeter is an embed target interface for MethodSet coverage.
type Greeter interface {
	Greet() string
}

// GreetBase declares the promoted method.
type GreetBase struct{}

// Greet is promoted through embedding.
func (GreetBase) Greet() string { return "base" }

// GreetEmbed promotes Greet through a by-value embed.
type GreetEmbed struct {
	GreetBase
}

// GreetPtr has only a pointer-receiver method.
type GreetPtr struct{}

// PtrOnly is visible only through pointer embeddings.
func (*GreetPtr) PtrOnly() {}

// GreetPtrEmbed promotes PtrOnly through a pointer embed.
type GreetPtrEmbed struct {
	*GreetPtr
}

// GreetValEmbed cannot see PtrOnly — value method sets exclude
// pointer receivers.
type GreetValEmbed struct {
	GreetPtr
}

// GreetIface promotes the Greeter spec through an interface embed.
type GreetIface struct {
	Greeter
}

// GB is an alias embed — the method set resolves through it.
type GB = GreetBase

// GreetAliasEmbed promotes through an alias.
type GreetAliasEmbed struct {
	GB
}

// CycA and CycB embed each other through pointers — the walk must
// terminate on the cycle.
type CycA struct {
	*CycB
}

type CycB struct {
	*CycA
}

// GreetShadow declares Greet itself and embeds GreetBase — the
// declared method wins the name.
type GreetShadow struct {
	GreetBase
}

// Greet shadows the promoted spelling.
func (GreetShadow) Greet() string { return "shadow" }

// GreetTalker embeds Greeter and adds a named spec — implementers
// must cover BOTH the embedded and the declared requirements.
type GreetTalker interface {
	Greeter
	Talk() string
}

// OnlyTalk has Talk alone — the missing Greet must fail it.
type OnlyTalk struct{}

// Talk matches the named spec only.
func (OnlyTalk) Talk() string { return "talk" }

// BothTalk has both — it satisfies.
type BothTalk struct{}

// Talk matches the named spec.
func (BothTalk) Talk() string { return "talk" }

// Greet matches the embedded spec.
func (BothTalk) Greet() string { return "both" }

// Composite aliases name no type — none may borrow GreetBase's set.
type SliceAlias = []GreetBase

// MapAlias is a composite alias — no borrowing.
type MapAlias = map[string]GreetBase

// FuncAlias is a composite alias — no borrowing.
type FuncAlias = func(GreetBase) int

// PtrAlias borrows legitimately: *GreetBase carries Greet.
type PtrAlias = *GreetBase

// ChainB is the deepest embed in the ptrEmbed-propagation chain.
type ChainB struct{}

// ValM is a value receiver — promoted through either embed shape.
func (ChainB) ValM() {}

// PtrM is a pointer receiver — reachable only through a pointer path.
func (*ChainB) PtrM() {}

// ChainA embeds ChainB by value.
type ChainA struct {
	ChainB
}

// ChainS embeds ChainA by pointer — PtrM survives both hops
// (var s ChainS; s.PtrM() compiles via s.ChainA.ChainB.PtrM()).
type ChainS struct {
	*ChainA
}

// DeepX sits two embed hops below ShadowS.
type DeepX struct{}

// M is the deeper promotion spelling (depth 2 through MidA).
func (DeepX) M() int { return 0 }

// MidA carries DeepX's members one level down.
type MidA struct {
	DeepX
}

// ShallowY has M at depth 1.
type ShallowY struct{}

// M shadows the deeper DeepX.M by shallowness.
func (ShallowY) M() string { return "shallow" }

// MStr is satisfied through ShallowY, never through DeepX.
type MStr interface {
	M() string
}

// ShadowS embeds MidA (M at depth 2) and ShallowY (M at depth 1) —
// the shallower spelling must win the name.
type ShadowS struct {
	MidA
	ShallowY
}

// Adder is a signature-shaped interface for Implementers coverage.
type Adder interface {
	Add(a, b int) int
}

// Calc satisfies Adder exactly.
type Calc struct{}

// Add matches the spec — same signature, position for position.
func (Calc) Add(a, b int) int { return a + b }

// Almost has the right name, wrong signature — must not satisfy.
type Almost struct{}

// Add returns string, not int.
func (Almost) Add(a, b int) string { return "no" }

// Summer is a variadic interface — the flag must match too.
type Summer interface {
	Sum(xs ...int) int
}

// SumImpl implements it.
type SumImpl struct{}

func (SumImpl) Sum(xs ...int) int { return 0 }

// SumArr has the right name but a fixed array, not variadic.
type SumArr struct{}

func (SumArr) Sum(xs [2]int) int { return 0 }

type hidden struct{ x int }

var hiddenVar = 1

// Path deliberately collides with the *runtime.Package.Path metadata
// field — member access resolves the package namespace first.
var Path = "member-shadow"

func main() {}

// ErrTalker requires the embedded `error` spec — Error() string —
// plus Talk; a Talk-only type must not satisfy it.
type ErrTalker interface {
	error
	Talk() string
}

// TalkErr declares Talk and Error — it satisfies ErrTalker (OnlyTalk
// and BothTalk, which lack Error, must not).
type TalkErr struct{}

// Talk matches the named spec.
func (TalkErr) Talk() string { return "talk" }

// Error covers the embedded error spec.
func (TalkErr) Error() string { return "boom" }

// TalkerAlias spells Talker through an alias — embedding the alias
// must still promote the target's specs.
type TalkerAlias = Talker

// AliasEmbedder embeds the alias spelling; its method set promotes
// Talker's Speak + Talk specs.
type AliasEmbedder interface {
	TalkerAlias
}

// Winner is the ambiguity fixture's requirement.
type Winner interface {
	W() int
}

// AmbA and AmbB declare W at the same promotion depth — AmbS's W
// selector is ambiguous, and Go excludes the member entirely.
type AmbA struct{}

// W claims the name for AmbA.
func (AmbA) W() int { return 1 }

type AmbB struct{}

// W claims the name for AmbB.
func (AmbB) W() int { return 2 }

// AmbS embeds both — W must NOT appear in its method set.
type AmbS struct {
	AmbA
	AmbB
}

// DiaBase's W reaches DiaS through two paths but as the same member —
// a diamond embed is not a conflict.
type DiaBase struct{}

// W is the shared member both paths reach.
func (DiaBase) W() int { return 0 }

type DiaA struct{ DiaBase }
type DiaB struct{ DiaBase }

// DiaS embeds both sides of the diamond — W stays in its method set.
type DiaS struct {
	DiaA
	DiaB
}

// AliasCalc declares Add over alias spellings — AInt collapses to
// int, so it satisfies Adder exactly like Calc.
type AliasCalc struct{}

// Add is spelled with the alias on both sides.
func (AliasCalc) Add(a, b AInt) AInt { return 0 }

// AAdder spells the Add requirement through the alias — identical to
// Adder for implementer purposes.
type AAdder interface {
	Add(a, b AInt) AInt
}

// MyIntCalc declares Add over a DEFINED type — MyInt is not int, so
// it must NOT satisfy either interface.
type MyIntCalc struct{}

// Add keeps the defined type distinct.
func (MyIntCalc) Add(a, b MyInt) MyInt { return 0 }

// Totaler is the nested case: alias elements inside a composite
// parameter spelling collapse too.
type Totaler interface {
	Total(xs []int) int
}

// AliasTotal takes []AInt — it satisfies Totaler.
type AliasTotal struct{}

// Total is spelled with the aliased element type.
func (AliasTotal) Total(xs []AInt) int { return 0 }

// WalkMe is a body-traversal fixture: a call with a literal arg, a
// func literal holding a nested call, and an import-anchored call, so
// Body/Nodes/Expr coverage has registerRoutes-shaped nesting.
func WalkMe(m *strings.Builder) {
	m.WriteString("a")
	helper := func(x string) {
		m.WriteString("b" + x)
	}
	helper(strings.TrimSpace("c"))
	if m != nil {
		m.WriteString("d")
	}
}

// NoBody is a bodyless func decl — parseable but uncompilable; Body
// reports nil for it.
func NoBody()

// ShadowMe binds a param named after the imported package, so the
// body's `strings.TrimSpace` is a method call on the local, not the
// package member — Expr-fed SymbolID must report not-ok for it.
func ShadowMe(strings string) string {
	return strings.TrimSpace(" x ")
}

// LocalDecls declares types, vars and a const inside its body; Expr-fed
// SymbolID must not attribute them to the package (#920), while the
// package type User still resolves.
func LocalDecls() int {
	type local struct{ B int }
	type other struct{}
	var x local
	var y local
	var z other
	var u User
	const k = 1
	_, _, _ = y, z, u
	return x.B + k
}

// LocalDecls2 declares its own local, a different type from
// LocalDecls's same-named one.
func LocalDecls2() {
	type local struct{ B int }
	var w local
	_ = w
}

// max shadows the predeclared max within this package.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// BuiltinNames spells predeclared funcs and constants in its body; they
// report the :builtin: SymbolID, except max, which the package shadows.
func BuiltinNames() int {
	s := make([]int, 0)
	s = append(s, len("ab"), cap(s))
	var e error = nil
	const c = iota
	_, _ = e, true
	return max(len(s), c)
}

// Pt, Lookup and KeyA feed NonRefIdents.
type Pt struct{ X, Y int }

type Lookup map[string]int

const KeyA = "a"

// NonRefIdents spells identifiers that name no declaration — selector
// Sels, struct-literal keys, labels, field names, the blank — next to
// map-literal keys that do refer to KeyA.
func NonRefIdents() int {
	p := Pt{X: 1, Y: 2}
	m := map[string]int{KeyA: 1}
	l := Lookup{KeyA: 2}
	_ = l
L:
	for i := 0; i < 1; i++ {
		break L
	}
	var s struct{ Z int }
	return p.X + m[KeyA] + s.Z + len(strings.Repeat("a", 1))
}
