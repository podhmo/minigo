package main

import (
	"fmt"
	"sort"
	"strings"
)

// The reflect domain differs from num/text: a probe is not one expression
// but a statement sequence — a seed value pulled into the facade
// (reflect.ValueOf/TypeOf/New) followed by a chain of reflect operations.
// Each step either derives a new value (`v1 := v0.Elem()`), mutates in
// place (`v0.Field(0).SetInt(3)`), or observes (`return v2.Kind()`).
//
// Divergence surface: the facade must reproduce Go's panic boundaries
// (Set on unaddressable, wrong-kind accessors), so ops that legitimately
// panic are part of the corpus — the oracle is `go run` itself.

// rstep is one link in the chain. Text is the method-call suffix
// (".Field(0)") unless Wrap is set, in which case it is a package-level
// call applied to the previous value ("reflect.Indirect"). Printf marks
// an observation rendered as fmt.Sprintf — needed for Interface(): its
// %T would diverge trivially (minigo's int is int64), while %#v prints
// the same digits on both sides. With Text also set, Printf renders the
// call result instead ("%v" of v.Len()), hiding int-vs-int64 the same
// way — this is how int/uint64/uintptr-returning ops are observed.
type rstep struct {
	Text   string
	Kind   byte   // 'v' reflect.Value, 's' []reflect.Value, 't' reflect.Type, 'o' observable, 'x' void stmt
	Wrap   bool   // Text is a package func applied to the previous var
	Printf string // "v" or "#v": render `vN := fmt.Sprintf("%<Printf>", <v><Text> or v.Interface())`
}

// rchain is a reflect probe: seed expression + ordered steps. Steps after
// a void step keep operating on the last non-void variable.
type rchain struct {
	Seed  string
	Steps []rstep
}

func (r *rchain) body() string {
	var b strings.Builder
	fmt.Fprintf(&b, "v0 := %s", r.Seed)
	v := "v0"
	for i, s := range r.Steps {
		nv := fmt.Sprintf("v%d", i+1)
		switch {
		case s.Kind == 'x':
			fmt.Fprintf(&b, "; %s%s", v, s.Text)
		case s.Printf != "":
			src := v + s.Text
			if s.Text == "" {
				src = v + ".Interface()"
			}
			fmt.Fprintf(&b, "; %s := fmt.Sprintf(\"%%%s\", %s)", nv, s.Printf, src)
			v = nv
		case s.Wrap:
			fmt.Fprintf(&b, "; %s := %s(%s)", nv, s.Text, v)
			v = nv
		default:
			fmt.Fprintf(&b, "; %s := %s%s", nv, v, s.Text)
			v = nv
		}
	}
	fmt.Fprintf(&b, "; return %s", v)
	return b.String()
}

// reflSeeds are the package-level values probes start from. Mutation probes
// use reflect.New copies so one probe's Set cannot bleed into the next.
const reflDecls = `type RInner struct{ X int }
type ROuter struct {
	RInner
	B int
}
type RTag struct {
	A int    ` + "`json:\"a\"`" + `
	B string ` + "`json:\"b,omitempty\"`" + `
}
type RNamed int
type RHidden struct {
	x int
	Y int
}

var (
	r_i0       = 42
	r_s0       = "hi"
	r_f0       = 1.5
	r_b0       = true
	r_slice    = []int{3, 4, 5}
	r_arr      = [3]int{1, 2, 3}
	r_map      = map[string]int{"a": 1, "b": 2}
	r_struct   = RTag{A: 7, B: "t"}
	r_outer    = ROuter{RInner: RInner{X: 9}, B: 8}
	r_hidden   = RHidden{x: 1, Y: 2}
	r_named    = RNamed(5)
	r_iface    = any("iface")
	r_nilptr   = (*int)(nil)
	r_nilmap   = map[string]int(nil)
	r_bytes    = []byte("abc")
	r_chan     = make(chan int, 1)
	r_func     = func(x int) int { return x * 2 }
	r_funcv    = func(xs ...int) int { s := 0; for _, x := range xs { s += x }; return s }
	r_empty    = any(nil)
	r_strslice = []string{"p", "q"}
	r_sptr     = &r_struct
	r_nested   = [][]int{{1, 2}, {3}}
	r_mapslice = map[string][]int{"k": {4, 5}}
)

// r_mkchan gives each probe its own buffered, prefilled channel — a
// shared channel would carry Sends across probes (cap-1 would block the
// whole program) and a bare Recv could block forever.
func r_mkchan() chan int { c := make(chan int, 32); c <- 7; return c }

// rTryRecvV / rTrySendB probe the non-blocking channel ops as values
// (Recv itself can block, so it is never probed).
func rTryRecvV(v reflect.Value) reflect.Value {
	r, ok := v.TryRecv()
	if !ok {
		return reflect.ValueOf("none")
	}
	return r
}

func rTrySendB(v reflect.Value) string {
	return fmt.Sprintf("%v", v.TrySend(reflect.ValueOf(9)))
}

// rAssertInt / rAssertStr probe reflect.TypeAssert[T] — a two-result
// generic call the chain model cannot express directly.
func rAssertInt(v reflect.Value) string {
	n, ok := reflect.TypeAssert[int](v)
	return fmt.Sprintf("%d %v", n, ok)
}

func rAssertStr(v reflect.Value) string {
	s, ok := reflect.TypeAssert[string](v)
	return fmt.Sprintf("%q %v", s, ok)
}

// rMethodType probes Type.MethodByName — also a two-result call.
func rMethodType(t reflect.Type) reflect.Type {
	m, ok := t.MethodByName("String")
	if !ok {
		return t
	}
	return m.Type
}
`

// seedExprs are the chain starters. Each entry is the full seed expression;
// addressable/mutable variants go through reflect.New on the same var's
// type so seeds stay per-probe.
var reflSeedExprs = []string{
	"reflect.ValueOf(r_i0)",
	"reflect.ValueOf(r_s0)",
	"reflect.ValueOf(r_f0)",
	"reflect.ValueOf(r_b0)",
	"reflect.ValueOf(r_slice)",
	"reflect.ValueOf(r_arr)",
	"reflect.ValueOf(r_map)",
	"reflect.ValueOf(r_struct)",
	"reflect.ValueOf(r_outer)",
	"reflect.ValueOf(r_hidden)",
	"reflect.ValueOf(r_named)",
	"reflect.ValueOf(r_iface)",
	"reflect.ValueOf(r_nilptr)",
	"reflect.ValueOf(r_nilmap)",
	"reflect.ValueOf(r_bytes)",
	"reflect.ValueOf(r_mkchan())",
	"reflect.ValueOf(r_func)",
	"reflect.ValueOf(r_funcv)",
	"reflect.ValueOf(r_empty)",
	"reflect.ValueOf(r_strslice)",
	"reflect.ValueOf(&r_i0).Elem()",
	"reflect.ValueOf(&r_s0).Elem()",
	"reflect.ValueOf(&r_slice).Elem()",
	"reflect.ValueOf(&r_map).Elem()",
	"reflect.ValueOf(&r_struct).Elem()",
	"reflect.ValueOf(&r_outer).Elem()",
	"reflect.ValueOf(&r_hidden).Elem()",
	"reflect.ValueOf(&r_named).Elem()",
	"reflect.ValueOf(&r_bytes).Elem()",
	"reflect.New(reflect.TypeOf(r_struct)).Elem()",
	"reflect.New(reflect.TypeOf(r_outer)).Elem()",
	"reflect.New(reflect.TypeOf(r_hidden)).Elem()",
	"reflect.New(reflect.TypeOf(r_i0)).Elem()",
	"reflect.New(reflect.TypeOf(r_map)).Elem()",
	"reflect.New(reflect.TypeOf(r_slice)).Elem()",
	"reflect.New(reflect.TypeOf(r_named)).Elem()",
	"reflect.TypeOf(r_struct)",
	"reflect.TypeOf(r_outer)",
	"reflect.TypeOf(r_hidden)",
	"reflect.TypeOf(r_slice)",
	"reflect.TypeOf(r_map)",
	"reflect.TypeOf(r_named)",
	"reflect.TypeOf(r_chan)",
	"reflect.TypeOf(r_func)",
	"reflect.TypeOf(r_funcv)",
	"reflect.TypeOf(r_empty)",
	"reflect.TypeOf(r_nilptr)",
	"reflect.Zero(reflect.TypeOf(r_slice))",
	"reflect.Zero(reflect.TypeOf(r_map))",
	"reflect.Zero(reflect.TypeOf(r_struct))",
	"reflect.Indirect(reflect.ValueOf(r_sptr))",
	"reflect.ValueOf(r_sptr)",
	"reflect.ValueOf(r_sptr).Elem()",
	"reflect.ValueOf(r_nilptr).Elem()",
	"reflect.ValueOf(&r_i0)",
	"reflect.ValueOf(r_nested)",
	"reflect.ValueOf(r_mapslice)",
	"reflect.TypeOf((*error)(nil)).Elem()",
	"reflect.TypeOf((*fmt.Stringer)(nil)).Elem()",
	"reflect.Zero(reflect.TypeOf((*error)(nil)).Elem())",
}

// --- step tables -----------------------------------------------------------
//
// Steps are picked by the current receiver kind: 'v' chains operate on a
// reflect.Value, 't' on a reflect.Type. Steps returning 'v' keep the chain
// going; 'x' steps are statements (mutators); 'o' steps are terminal
// observations. A few deliberately-unsupported calls (NewAt, MakeChan)
// enter at low weight — they must trap cleanly, not silently diverge.

// sliceSteps unwrap a 's' ([]reflect.Value) chain into elements.
// The slice itself is never observed: %#v of reflect.Value prints
// internal pointer fields that can never match across implementations.
var sliceSteps = []rstep{
	{Text: "[0]", Kind: 'v'},
	{Text: "[0]", Kind: 'v'},
	{Text: "[1]", Kind: 'v'},
	{Text: "[2]", Kind: 'v'},
}

// valueSteps continue a 'v' chain. Args are baked into Text; arg values
// come from small literals or seed vars (both deterministic).
var valueSteps = []rstep{
	// navigation — keep 'v'
	{Text: ".Elem()", Kind: 'v'},
	{Text: ".Elem()", Kind: 'v'},
	{Text: ".Field(0)", Kind: 'v'},
	{Text: ".Field(1)", Kind: 'v'},
	{Text: ".Field(2)", Kind: 'v'},
	{Text: ".FieldByName(\"A\")", Kind: 'v'},
	{Text: ".FieldByName(\"X\")", Kind: 'v'},
	{Text: ".FieldByName(\"x\")", Kind: 'v'},
	{Text: ".FieldByName(\"nosuch\")", Kind: 'v'},
	{Text: ".Index(0)", Kind: 'v'},
	{Text: ".Index(1)", Kind: 'v'},
	{Text: ".Index(9)", Kind: 'v'},
	{Text: ".Index(-1)", Kind: 'v'},
	{Text: ".MapIndex(reflect.ValueOf(\"a\"))", Kind: 'v'},
	{Text: ".MapIndex(reflect.ValueOf(\"nosuch\"))", Kind: 'v'},
	{Text: ".MapIndex(reflect.ValueOf(3))", Kind: 'v'},
	{Text: ".MapKeys()", Kind: 's'},
	{Text: ".Method(0)", Kind: 'v'},
	{Text: ".MethodByName(\"String\")", Kind: 'v'},
	// .Addr() is excluded: %#v of a pointer prints a raw address that
	// differs every run — a permanent nondeterministic finding.
	{Text: ".Slice(0, 1)", Kind: 'v'},
	{Text: ".Slice(1, 3)", Kind: 'v'},
	{Text: ".Slice(0, 99)", Kind: 'v'},
	{Text: ".Convert(reflect.TypeOf(r_named))", Kind: 'v'},
	{Text: ".Convert(reflect.TypeOf(r_i0))", Kind: 'v'},
	{Text: ".Convert(reflect.TypeOf(r_s0))", Kind: 'v'},
	{Text: ".Type()", Kind: 't'},
	{Text: "reflect.Indirect", Kind: 'v', Wrap: true},
	{Text: "rTryRecvV", Kind: 'v', Wrap: true},
	// calls — variadic and fixed
	// Call/CallSlice return []reflect.Value — a 's' receiver the chain
	// must unwrap via .Index before observing.
	{Text: ".Call([]reflect.Value{reflect.ValueOf(3)})", Kind: 's'},
	{Text: ".Call([]reflect.Value{})", Kind: 's'},
	{Text: ".CallSlice([]reflect.Value{reflect.ValueOf(r_slice)})", Kind: 's'},
	// mutators — 'x' statements; correctness shows up via a later
	// observation of the same (or seed) object.
	{Text: ".SetInt(7)", Kind: 'x'},
	{Text: ".SetUint(9)", Kind: 'x'},
	{Text: ".SetString(\"zz\")", Kind: 'x'},
	{Text: ".SetBool(true)", Kind: 'x'},
	{Text: ".SetFloat(2.5)", Kind: 'x'},
	{Text: ".SetBytes([]byte(\"zz\"))", Kind: 'x'},
	{Text: ".Set(reflect.ValueOf(11))", Kind: 'x'},
	{Text: ".Set(reflect.ValueOf(r_i0))", Kind: 'x'},
	{Text: ".SetMapIndex(reflect.ValueOf(\"c\"), reflect.ValueOf(3))", Kind: 'x'},
	{Text: ".SetMapIndex(reflect.ValueOf(9), reflect.ValueOf(3))", Kind: 'x'},
	{Text: ".SetLen(2)", Kind: 'x'},
	{Text: ".SetCap(9)", Kind: 'x'},
	{Text: ".Send(reflect.ValueOf(4))", Kind: 'x'},
	// void-adjacent: Recv returns (Value, bool) — bind as a 2-result call
	{Text: ".Close()", Kind: 'x'},
	// intentionally-unsupported surface and boundary accessors — must
	// panic identically to reflect's own panics.
	{Text: ".SetIterKey(reflect.ValueOf(r_map).MapRange())", Kind: 'x'},
	{Text: ".SetIterValue(reflect.ValueOf(r_map).MapRange())", Kind: 'x'},
}

// valueObs are terminal observations on a reflect.Value — restricted to
// result types whose %T is identical on both sides (string, bool,
// reflect.Kind, int64, float64). Int-returning ops (Len/NumField/...) and
// uintptr (Pointer/UnsafeAddr) would diverge trivially as int-vs-int64;
// Interface() goes through Sprintf normalization instead.
var valueObs = []rstep{
	{Text: ".Kind()", Kind: 'o'},
	{Text: ".Kind()", Kind: 'o'},
	{Text: ".Int()", Kind: 'o'},
	{Text: ".Float()", Kind: 'o'},
	{Text: ".Bool()", Kind: 'o'},
	{Text: ".String()", Kind: 'o'},
	{Text: ".IsNil()", Kind: 'o'},
	{Text: ".IsZero()", Kind: 'o'},
	{Text: ".IsValid()", Kind: 'o'},
	{Text: ".CanSet()", Kind: 'o'},
	{Text: ".CanAddr()", Kind: 'o'},
	{Text: ".CanInterface()", Kind: 'o'},
	{Text: ".Type().Name()", Kind: 'o'},
	{Text: ".Type().String()", Kind: 'o'},
	{Text: ".Type().PkgPath()", Kind: 'o'},
	{Text: ".Type().Comparable()", Kind: 'o'},
	{Text: ".OverflowInt(1)", Kind: 'o'},
	{Text: ".OverflowFloat(1.5)", Kind: 'o'},
	{Text: ".OverflowUint(3)", Kind: 'o'},
	{Text: ".Equal(reflect.ValueOf(3))", Kind: 'o'},
	{Text: ".Equal(reflect.ValueOf(r_i0))", Kind: 'o'},
	{Text: ".Complex()", Kind: 'o'},
	// int/uint64-returning accessors render via %v — their %T would
	// diverge trivially (int vs int64).
	{Text: ".Len()", Kind: 'o', Printf: "v"},
	{Text: ".Cap()", Kind: 'o', Printf: "v"},
	{Text: ".NumField()", Kind: 'o', Printf: "v"},
	{Text: ".NumMethod()", Kind: 'o', Printf: "v"},
	{Text: ".Bytes()", Kind: 'o', Printf: "v"},
	{Text: ".Uint()", Kind: 'o', Printf: "v"},
	{Text: "rTrySendB", Kind: 'o', Wrap: true},
	{Text: "rAssertInt", Kind: 'o', Wrap: true},
	{Text: "rAssertStr", Kind: 'o', Wrap: true},
	{Kind: 'o', Printf: "v"},
	{Kind: 'o', Printf: "#v"},
	{Kind: 'o', Printf: "#v"},
}

// typeSteps continue a 't' chain. FieldByName is absent: on reflect.Type
// it returns (StructField, bool), which needs a two-value bind the chain
// model cannot express.
var typeSteps = []rstep{
	{Text: ".Elem()", Kind: 't'},
	{Text: ".Elem()", Kind: 't'},
	{Text: ".Key()", Kind: 't'},
	{Text: ".Field(0).Type", Kind: 't'},
	{Text: ".Field(1).Type", Kind: 't'},
	{Text: ".Method(0).Type", Kind: 't'},
	{Text: ".In(0)", Kind: 't'},
	{Text: ".Out(0)", Kind: 't'},
	{Text: ".FieldByIndex([]int{0}).Type", Kind: 't'},
	{Text: ".FieldByIndex([]int{0, 0}).Type", Kind: 't'},
	{Text: ".FieldByIndex([]int{0, 1}).Type", Kind: 't'},
	{Text: ".Method(0).Func", Kind: 'v'},
	{Text: "rMethodType", Kind: 't', Wrap: true},
}

// typeObs are terminal observations on a reflect.Type — same %T-safe
// rule (int-returning Num*/Len/Bits excluded).
var typeObs = []rstep{
	{Text: ".Kind()", Kind: 'o'},
	{Text: ".Kind()", Kind: 'o'},
	{Text: ".Name()", Kind: 'o'},
	{Text: ".String()", Kind: 'o'},
	{Text: ".String()", Kind: 'o'},
	{Text: ".PkgPath()", Kind: 'o'},
	{Text: ".Comparable()", Kind: 'o'},
	{Text: ".IsVariadic()", Kind: 'o'},
	{Text: ".Implements(reflect.TypeOf(r_func))", Kind: 'o'},
	{Text: ".Implements(reflect.TypeOf(r_i0))", Kind: 'o'},
	{Text: ".AssignableTo(reflect.TypeOf(r_i0))", Kind: 'o'},
	{Text: ".AssignableTo(reflect.TypeOf(r_named))", Kind: 'o'},
	{Text: ".ConvertibleTo(reflect.TypeOf(r_named))", Kind: 'o'},
	{Text: ".ConvertibleTo(reflect.TypeOf(r_i0))", Kind: 'o'},
	{Text: ".Field(0).Tag.Get(\"json\")", Kind: 'o'},
	{Text: ".Field(1).Tag.Get(\"json\")", Kind: 'o'},
	{Text: ".FieldByIndex([]int{9}).Name", Kind: 'o'},
	{Text: ".Field(0).Name", Kind: 'o'},
	{Text: ".Field(0).Anonymous", Kind: 'o'},
	{Text: ".Method(0).Name", Kind: 'o'},
	{Text: ".ChanDir()", Kind: 'o', Printf: "v"},
	// int/uintptr-returning Type accessors render via %v, same rule as
	// the Value int accessors above.
	{Text: ".Bits()", Kind: 'o', Printf: "v"},
	{Text: ".Align()", Kind: 'o', Printf: "v"},
	{Text: ".FieldAlign()", Kind: 'o', Printf: "v"},
	{Text: ".Len()", Kind: 'o', Printf: "v"},
	{Text: ".NumField()", Kind: 'o', Printf: "v"},
	{Text: ".NumMethod()", Kind: 'o', Printf: "v"},
	{Text: ".Field(0).Index", Kind: 'o', Printf: "v"},
	{Text: ".Field(0).Offset", Kind: 'o', Printf: "v"},
}

// --- probe generation ------------------------------------------------------

var reflDomain = &Domain{
	Name:    "reflect",
	Imports: []string{"fmt", "reflect"},
	Decls:   []string{reflDecls},
}

// seedKind reports which receiver kind a seed feeds: only a bare
// reflect.TypeOf seed yields a Type — New/Zero/Indirect/ValueOf all
// return reflect.Value.
func seedKind(seed string) byte {
	if strings.HasPrefix(seed, "reflect.TypeOf(") {
		return 't'
	}
	return 'v'
}

func (g *Gen) reflProbe(depth int) Probe {
	seed := reflSeedExprs[g.R.IntN(len(reflSeedExprs))]
	kind := seedKind(seed)
	r := &rchain{Seed: seed}
	steps := 1 + g.R.IntN(depth+2)
	for i := 0; i < steps; i++ {
		s := g.reflStep(kind)
		r.Steps = append(r.Steps, s)
		if s.Kind != 'x' {
			kind = s.Kind
		}
	}
	r.Steps = append(r.Steps, g.reflFinish(kind)...)
	return Probe{D: reflDomain, Ctx: "seq", R: r}
}

// reflStep picks one non-terminal step for the current receiver kind.
func (g *Gen) reflStep(kind byte) rstep {
	var s rstep
	for {
		switch kind {
		case 'v':
			s = valueSteps[g.R.IntN(len(valueSteps))]
		case 't':
			s = typeSteps[g.R.IntN(len(typeSteps))]
		case 's':
			s = sliceSteps[g.R.IntN(len(sliceSteps))]
		}
		// 'o' steps are terminals only — never drawn mid-chain.
		if s.Kind != 'o' {
			return s
		}
	}
}

// reflFinish renders the terminal steps turning the current receiver
// into an observable: unwrap []Value, then a kind-matched observation.
func (g *Gen) reflFinish(kind byte) []rstep {
	if kind == 's' {
		return []rstep{{Text: "[0]", Kind: 'v'}, {Kind: 'o', Printf: "#v"}}
	}
	return []rstep{g.reflObsFor(kind)}
}

// reflObsFor picks the terminal observation matching a receiver kind.
// 's' receivers must be unwrapped by the caller (reflFinish does).
func (g *Gen) reflObsFor(kind byte) rstep {
	if kind == 't' {
		return typeObs[g.R.IntN(len(typeObs))]
	}
	return valueObs[g.R.IntN(len(valueObs))]
}

// shrinkFinish is the deterministic terminal for truncated chains — a
// fixed observation per kind so shrinks stay stable.
func shrinkFinish(kind byte) []rstep {
	if kind == 's' {
		return []rstep{{Text: "[0]", Kind: 'v'}, {Kind: 'o', Printf: "#v"}}
	}
	if kind == 't' {
		return []rstep{{Text: ".Kind()", Kind: 'o'}}
	}
	return []rstep{{Kind: 'o', Printf: "#v"}}
}

// --- shrinking -------------------------------------------------------------

// reflShrink is the chain analogue of the expression shrinker: truncate
// the chain at every point (re-terminalizing the truncated receiver),
// drop each void step, and swap the seed for the simplest form.
func reflShrink(p Probe) []Probe {
	var out []Probe
	seen := map[string]bool{p.Body(): true}
	add := func(r *rchain) {
		if len(r.Steps) == 0 || r.Steps[len(r.Steps)-1].Kind != 'o' {
			return // must end observable
		}
		q := Probe{D: p.D, Ctx: p.Ctx, R: r}
		if k := q.Body(); !seen[k] {
			seen[k] = true
			out = append(out, q)
		}
	}
	// truncate at every position, re-terminalizing
	kind := seedKind(p.R.Seed)
	for k := 0; k < len(p.R.Steps)-1; k++ {
		s := p.R.Steps[k]
		if s.Kind != 'x' {
			kind = s.Kind
		}
		steps := append([]rstep{}, p.R.Steps[:k+1]...)
		if s.Kind != 'o' {
			steps = append(steps, shrinkFinish(kind)...)
		}
		add(&rchain{Seed: p.R.Seed, Steps: steps})
	}
	// drop each void step (mutation may be what hides the bug — or the
	// bug itself)
	for i, s := range p.R.Steps {
		if s.Kind != 'x' {
			continue
		}
		steps := append([]rstep{}, p.R.Steps[:i]...)
		steps = append(steps, p.R.Steps[i+1:]...)
		add(&rchain{Seed: p.R.Seed, Steps: steps})
	}
	// seed swap: same-kind seeds only — a 't' chain (In/Out/...) cannot
	// run on a reflect.Value seed or vice versa.
	sk := seedKind(p.R.Seed)
	for _, seed := range reflSeedExprs {
		if seedKind(seed) == sk && len(seed) < len(p.R.Seed) {
			steps := append([]rstep{}, p.R.Steps...)
			add(&rchain{Seed: seed, Steps: steps})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Size() < out[j].Size() })
	return out
}
