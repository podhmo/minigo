package main

// Promoted-method resolution: Go selects the promoted member at the
// shallowest embed depth and rejects same-depth collisions as an
// ambiguous selector. Expected values verified against `go run`
// (go1.27) except where a fixture is intentionally Go-invalid — those
// pin the runtime trap that stands in for Go's compile-time rejection.
//
// The interpreter's findMethod used to be DFS first-wins; the promoted
// member walk is now breadth-first like the field walk, and these cases
// pin both halves.

// ---- depth ordering ----

type DeepA struct{}

func (DeepA) M() string { return "deep" }

type DeepX struct{ DeepA }

type ShallowB struct{}

func (ShallowB) M() string { return "shallow" }

// ShadowS promotes DeepX.DeepA.M (depth 2) and ShallowB.M (depth 1).
// Go resolves the selector to ShallowB.M — declaration order does not
// matter, only depth.
type ShadowS struct {
	DeepX
	ShallowB
}

func ShadowOrder() string {
	var s ShadowS
	return s.M()
}

// The same selector feeds method expressions — Go lowers S.M to a
// re-selection on the receiver.
func ShadowMethodExpr() string {
	var s ShadowS
	return ShadowS.M(s)
}

// And interface dispatch — i.M selects on the stored struct.
type MUser interface{ M() string }

func ShadowIface() string {
	var s ShadowS
	var i MUser = s
	return i.M()
}

// A bound method value keeps the same receiver choice.
func ShadowBoundMethod() string {
	var s ShadowS
	f := s.M
	return f()
}

// ---- same-depth ambiguity (compile-time rejection in Go) ----

type AmbY struct{ DeepA }
type AmbZ struct{ DeepA }

type AmbS struct {
	AmbY
	AmbZ
}

// Go: ambiguous selector — already trapped before this change.
func Ambig() string {
	var s AmbS
	return s.M()
}

// Two interface embeds both promising M collide the same way — a
// struct{I1;I2} value can never select M, whatever it stores.
type IM1 interface{ M() string }
type IM2 interface{ M() string }

type IfaceAmbS struct {
	IM1
	IM2
}

func IfaceAmbig() string {
	var s IfaceAmbS
	return s.M()
}

// An interface spec and a declared method at the same depth collide
// too — Go does not privilege either kind.
type IfaceMixS struct {
	IM1
	ShallowB
}

func IfaceMethodAmbig() string {
	var s IfaceMixS
	return s.M()
}

// ---- interface embeds still dispatch dynamically on a single path ----

type IfaceOnlyS struct {
	IM1
}

func IfaceOnlyDispatch() string {
	s := IfaceOnlyS{IM1: ShallowB{}}
	return s.M()
}

// ---- nil embedded pointers ----

type PtrT struct{ v int }

func (t *PtrT) M() string {
	if t == nil {
		return "nil-recv"
	}
	return "recv"
}

type PtrS struct{ *PtrT }

// Go binds the nil *PtrT as the receiver — a pointer-receiver method
// promotes through a nil embed without dereferencing it.
func NilEmbedPtrRecv() string {
	var s PtrS
	return s.M()
}

type ValT struct{ v int }

func (t ValT) M() string { return "val" }

type ValS struct{ *ValT }

// A value-receiver method through a nil embed selects fine but panics
// on the implicit dereference — already correct before this change.
func NilEmbedValRecv() string {
	var s ValS
	return s.M()
}

// ---- nil chains deeper than one hop ----

type PtrInner struct{ *PtrT }
type PtrOuter struct{ *PtrInner }

// The promoted receiver is o.PtrInner.PtrT — evaluating it dereferences
// the nil o.PtrInner, so the call panics rather than binding nil.
func NilEmbedPtrRecvDeep() string {
	var o PtrOuter
	return o.M()
}

// With the outer link live, the receiver s.PtrInner.PtrT is just a nil
// *PtrT — Go binds it as the receiver.
func NilEmbedPtrRecvMid() string {
	o := PtrOuter{PtrInner: &PtrInner{}}
	return o.M()
}

type ValInner struct{ *ValT }
type ValOuter struct{ *ValInner }

func NilEmbedValRecvDeep() string {
	var o ValOuter
	return o.M()
}

// ---- recursive embeds ----

// Go rejects recursive embedding outright; minigo accepts the type, so
// a missing member must trap instead of looping the walk forever.
type RecS struct {
	*RecS
	V int
}

func RecField() int {
	var s RecS
	s.V = 3
	return s.V
}

func RecNoMember() int {
	var s RecS
	_ = s.Nosuch
	return 0
}
