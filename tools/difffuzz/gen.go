package main

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
)

// Typ is a scalar type the generator knows how to produce. Named types
// (MyU8, MyI16) exercise the "mask through the underlying" path.
type Typ struct {
	Name     string
	Under    string // underlying predeclared type
	Bits     int    // 0 for non-integers
	Signed   bool
	Kind     string // "int", "float", "bool", "string"
	Values   []string
	Declared bool   // needs a `type Name Under` decl
	VarKey   string // variable-name stem; defaults to Name (two domains may share a Name)
}

func (t *Typ) key() string {
	if t.VarKey != "" {
		return t.VarKey
	}
	return t.Name
}

func intVals(bits int, signed bool) []string {
	if signed {
		max := fmt.Sprint(int64(1)<<(bits-1) - 1)
		min := fmt.Sprint(-(int64(1) << (bits - 1)))
		if bits == 64 {
			min = "-9223372036854775808"
		}
		return []string{"0", "1", "-1", "2", "7", "-3", max, min, "0x5a"}
	}
	max := fmt.Sprint(uint64(1)<<bits - 1)
	if bits == 64 {
		max = "18446744073709551615"
	}
	half := fmt.Sprint(uint64(1) << (bits - 1))
	return []string{"0", "1", "2", "7", "3", max, half, "0x5a"}
}

func intTyp(name string, bits int, signed bool) *Typ {
	return &Typ{Name: name, Under: name, Bits: bits, Signed: signed, Kind: "int", Values: intVals(bits, signed)}
}

var (
	tInt     = intTyp("int", 64, true)
	tUint    = intTyp("uint", 64, false)
	tFloat   = &Typ{Name: "float64", Under: "float64", Kind: "float", Values: []string{"0", "1.5", "-2.25", "1e21", "1e-7", "3", "0.1"}}
	tBool    = &Typ{Name: "bool", Under: "bool", Kind: "bool", Values: []string{"true", "false"}}
	tString  = &Typ{Name: "string", Under: "string", Kind: "string", Values: []string{`""`, `"a"`, `"héllo"`, `"Go"`, `"\x00z"`}}
	intTypes = []*Typ{
		tInt, intTyp("int8", 8, true), intTyp("int16", 16, true), intTyp("int32", 32, true), intTyp("int64", 64, true),
		tUint, intTyp("uint8", 8, false), intTyp("uint16", 16, false), intTyp("uint32", 32, false), intTyp("uint64", 64, false),
		intTyp("uintptr", 64, false),
		{Name: "MyU8", Under: "uint8", Bits: 8, Kind: "int", Values: intVals(8, false), Declared: true},
		{Name: "MyI16", Under: "int16", Bits: 16, Signed: true, Kind: "int", Values: intVals(16, true), Declared: true},
	}
	allTypes = append(append([]*Typ{}, intTypes...), tFloat, tBool, tString)
)

// Node is a typed expression tree. Leaves are either variable references
// (runtime values, so no constant folding/overflow checks by the compiler)
// or small untyped literals that fit every type.
type Node struct {
	T    *Typ
	Op   string // "var", "lit", unary op, binary op, "conv", "call:<name>", "assign:<op>", "incdec:<op>"
	Text string // leaf spelling
	Sig  *Sig   // for Op "call"
	Kids []*Node
}

func (n *Node) IsConst() bool { return n.Op == "lit" }

func (n *Node) Size() int {
	s := 1
	for _, k := range n.Kids {
		s += k.Size()
	}
	return s
}

// Expr renders the node as a Go expression.
func (n *Node) Expr() string {
	switch n.Op {
	case "var", "lit":
		return n.Text
	case "call":
		return n.Sig.render(n.Kids)
	case "conv":
		return n.T.Name + "(" + n.Kids[0].Expr() + ")"
	case "len":
		return "len(" + n.Kids[0].Expr() + ")"
	case "runestr":
		return "string(rune(" + n.Kids[0].Expr() + "))"
	case "sprint":
		return "fmt.Sprint(" + n.Kids[0].Expr() + ")"
	}
	if len(n.Kids) == 1 { // unary - ^ !
		return n.Op + "(" + n.Kids[0].Expr() + ")"
	}
	if len(n.Kids) == 2 {
		return "(" + n.Kids[0].Expr() + " " + n.Op + " " + n.Kids[1].Expr() + ")"
	}
	panic("unknown op " + n.Op)
}

// Shape is the dedup key: the expression with leaves replaced by types.
func (n *Node) Shape() string {
	switch n.Op {
	case "var":
		return n.T.Name
	case "lit":
		return "lit"
	}
	parts := []string{}
	for _, k := range n.Kids {
		parts = append(parts, k.Shape())
	}
	if n.Op == "call" {
		return n.Sig.Name + "(" + strings.Join(parts, ", ") + ")"
	}
	if n.Op == "conv" {
		return n.T.Name + "(" + parts[0] + ")"
	}
	if len(parts) == 1 {
		return n.Op + "(" + parts[0] + ")"
	}
	return "(" + parts[0] + " " + n.Op + " " + parts[1] + ")"
}

// Gen produces random well-typed expressions. Every expression it emits
// compiles under gc: constant-only subtrees never form (so no overflow or
// constant division-by-zero diagnostics), shift counts are unsigned, and
// float→int conversions (implementation-defined when out of range) are
// never generated.
type Gen struct {
	R *rand.Rand
	D *Domain
}

// Domain is a family of types plus the expression generator over them.
type Domain struct {
	Name    string
	Types   []*Typ
	Imports []string
	expr    func(g *Gen, t *Typ, depth int) *Node
}

var (
	numDomain  = &Domain{Name: "num", Types: allTypes, Imports: []string{"fmt"}}
	textDomain = &Domain{Name: "text", Types: textTypes, Imports: textImports}
	domains    = map[string]*Domain{"num": numDomain, "text": textDomain}
)

func init() {
	numDomain.expr = (*Gen).numExpr
	textDomain.expr = (*Gen).textExpr
}

func (g *Gen) pick(xs []*Typ) *Typ { return xs[g.R.IntN(len(xs))] }

func varName(t *Typ, i int) string { return fmt.Sprintf("v_%s_%d", t.key(), i) }

func (g *Gen) leaf(t *Typ) *Node {
	i := g.R.IntN(len(t.Values))
	return &Node{T: t, Op: "var", Text: varName(t, i)}
}

// smallLit is an untyped literal usable against any integer/float type.
func (g *Gen) smallLit(t *Typ) *Node {
	return &Node{T: t, Op: "lit", Text: fmt.Sprint(1 + g.R.IntN(9))}
}

func (g *Gen) Expr(t *Typ, depth int) *Node { return g.D.expr(g, t, depth) }

func (g *Gen) numExpr(t *Typ, depth int) *Node {
	if depth <= 0 || g.R.IntN(4) == 0 {
		return g.leaf(t)
	}
	switch t.Kind {
	case "int":
		return g.intExpr(t, depth)
	case "float":
		return g.floatExpr(t, depth)
	case "bool":
		return g.boolExpr(depth)
	default:
		return g.stringExpr(depth)
	}
}

func (g *Gen) operand(t *Typ, depth int, allowLit bool) *Node {
	if allowLit && g.R.IntN(5) == 0 {
		return g.smallLit(t)
	}
	return g.Expr(t, depth-1)
}

func (g *Gen) intExpr(t *Typ, depth int) *Node {
	switch g.R.IntN(10) {
	case 0, 1, 2, 3:
		ops := []string{"+", "-", "*", "&", "|", "^", "&^"}
		op := ops[g.R.IntN(len(ops))]
		l := g.Expr(t, depth-1)
		return &Node{T: t, Op: op, Kids: []*Node{l, g.operand(t, depth, true)}}
	case 4:
		op := []string{"/", "%"}[g.R.IntN(2)]
		l := g.Expr(t, depth-1)
		var r *Node
		if g.R.IntN(3) == 0 {
			r = g.smallLit(t) // non-zero constant divisor
		} else {
			r = g.Expr(t, depth-1) // may be zero/-1: recovered at runtime
		}
		return &Node{T: t, Op: op, Kids: []*Node{l, r}}
	case 5, 6:
		op := []string{"<<", ">>"}[g.R.IntN(2)]
		ct := g.pick([]*Typ{tUint, intTypes[6], intTypes[9], tInt, intTypes[1]}) // uint, uint8, uint64, int, int8
		var cnt *Node
		if g.R.IntN(3) == 0 {
			cnt = &Node{T: ct, Op: "lit", Text: fmt.Sprint(g.R.IntN(70))}
		} else {
			cnt = g.Expr(ct, depth-1)
		}
		return &Node{T: t, Op: op, Kids: []*Node{g.Expr(t, depth-1), cnt}}
	case 7:
		op := "-"
		if g.R.IntN(2) == 0 {
			op = "^"
		}
		return &Node{T: t, Op: op, Kids: []*Node{g.Expr(t, depth-1)}}
	case 8:
		src := g.pick(intTypes)
		return &Node{T: t, Op: "conv", Kids: []*Node{g.Expr(src, depth-1)}}
	default:
		if t == tInt {
			return &Node{T: t, Op: "len", Kids: []*Node{g.Expr(tString, depth-1)}}
		}
		return &Node{T: t, Op: "conv", Kids: []*Node{g.Expr(g.pick(intTypes), depth-1)}}
	}
}

func (g *Gen) floatExpr(t *Typ, depth int) *Node {
	switch g.R.IntN(4) {
	case 0, 1:
		op := []string{"+", "-", "*", "/"}[g.R.IntN(4)]
		return &Node{T: t, Op: op, Kids: []*Node{g.Expr(t, depth-1), g.operand(t, depth, true)}}
	case 2:
		return &Node{T: t, Op: "-", Kids: []*Node{g.Expr(t, depth-1)}}
	default:
		return &Node{T: t, Op: "conv", Kids: []*Node{g.Expr(g.pick(intTypes), depth-1)}}
	}
}

func (g *Gen) boolExpr(depth int) *Node {
	switch g.R.IntN(4) {
	case 0:
		op := []string{"&&", "||", "==", "!="}[g.R.IntN(4)]
		return &Node{T: tBool, Op: op, Kids: []*Node{g.Expr(tBool, depth-1), g.Expr(tBool, depth-1)}}
	case 1:
		return &Node{T: tBool, Op: "!", Kids: []*Node{g.Expr(tBool, depth-1)}}
	default:
		t := g.pick(append(append([]*Typ{}, intTypes...), tFloat, tString))
		op := []string{"==", "!=", "<", "<=", ">", ">="}[g.R.IntN(6)]
		return &Node{T: tBool, Op: op, Kids: []*Node{g.Expr(t, depth-1), g.Expr(t, depth-1)}}
	}
}

func (g *Gen) stringExpr(depth int) *Node {
	switch g.R.IntN(3) {
	case 0:
		return &Node{T: tString, Op: "+", Kids: []*Node{g.Expr(tString, depth-1), g.Expr(tString, depth-1)}}
	case 1:
		return &Node{T: tString, Op: "runestr", Kids: []*Node{g.Expr(intTypes[3], depth-1)}} // int32
	default:
		return &Node{T: tString, Op: "sprint", Kids: []*Node{g.Expr(g.pick(allTypes), depth-1)}}
	}
}

// Probe is one line of a generated program: an expression evaluated in one
// of several statement contexts, which reach different compile paths.
type Probe struct {
	D    *Domain
	Ctx  string // see Body
	Root *Node
}

// Valid reports whether the probe still compiles under gc after a shrink
// step: no constant-only operator nodes (overflow / constant-conversion
// diagnostics), no untyped constant as a shift's left operand, and no
// untyped constant feeding a `t := ...` declaration.
func (p Probe) Valid() bool {
	if p.Root.Op == "lit" || !noConstOps(p.Root) {
		return false
	}
	if p.Ctx == "assign" && len(p.Root.Kids) == 2 && p.Root.Kids[0].IsConst() {
		return false
	}
	return true
}

func noConstOps(n *Node) bool {
	if len(n.Kids) == 0 {
		return true
	}
	// `lit << v` takes its type from the context, not from the node.
	if (n.Op == "<<" || n.Op == ">>") && n.Kids[0].IsConst() {
		return false
	}
	allConst := true
	for _, k := range n.Kids {
		if !k.IsConst() {
			allConst = false
		}
		if !noConstOps(k) {
			return false
		}
	}
	return !allConst
}

func (p Probe) Size() int { return p.Root.Size() }

func (p Probe) Shape() string { return p.Ctx + ":" + p.Root.Shape() }

// Body renders the closure body producing the probe's value.
func (p Probe) Body() string {
	n := p.Root
	switch p.Ctx {
	case "assign": // t := a; t op= b — the compound-assign path
		if len(n.Kids) == 2 && n.T.Kind != "bool" && n.Op != "&&" && n.Op != "||" && n.Op != "call" && n.T == n.Kids[0].T {
			return fmt.Sprintf("t := %s; t %s= %s; return t", n.Kids[0].Expr(), n.Op, n.Kids[1].Expr())
		}
	case "incdec": // t := a; t++ / t--
		if n.T.Kind == "int" || n.T.Kind == "float" {
			op := "++"
			if n.Size()%2 == 0 {
				op = "--"
			}
			return fmt.Sprintf("t := %s; t%s; return t", n.Expr(), op)
		}
	// metamorphic contexts: the same value must survive each carrier,
	// which compile through different paths (generic instantiation,
	// struct literal + selector, closure call, deferred assignment).
	case "generic":
		return "return id(" + n.Expr() + ")"
	case "field":
		return fmt.Sprintf("return struct{ F %s }{F: %s}.F", n.T.Name, n.Expr())
	case "closure":
		return fmt.Sprintf("f := func() %s { return %s }; return f()", n.T.Name, n.Expr())
	case "defer":
		return fmt.Sprintf("var out %s; func() { defer func() { out = %s }() }(); return out", n.T.Name, n.Expr())
	}
	return "return " + n.Expr()
}

var metaCtxs = []string{"generic", "field", "closure", "defer"}

func (g *Gen) Probe(depth int) Probe {
	t := g.pick(g.D.Types)
	n := g.Expr(t, depth)
	for range 3 { // a bare variable tests nothing interesting
		if n.Op != "var" {
			break
		}
		n = g.Expr(t, depth)
	}
	ctx := "expr"
	switch x := g.R.IntN(10); {
	case x < 2 && g.D == numDomain:
		ctx = []string{"assign", "incdec"}[x]
	case x < 4:
		ctx = metaCtxs[g.R.IntN(len(metaCtxs))]
	}
	return Probe{D: g.D, Ctx: ctx, Root: n}
}

// Program renders probes as one Go program. Each probe runs in its own
// closure with a recover, so runtime panics (division by zero, negative
// shift counts) are part of the observed output instead of ending the run.
func Program(d *Domain, probes []Probe, ids []int) string {
	var body strings.Builder
	for i, p := range probes {
		fmt.Fprintf(&body, "\ttry(%d, func() any { %s })\n", ids[i], p.Body())
	}
	var b strings.Builder
	b.WriteString("package main\n\nimport (\n")
	// import only what the probes reference: unused imports don't compile
	// and a used-only-for-anchoring import could trap in minigo's init.
	for _, imp := range d.Imports {
		last := imp[strings.LastIndex(imp, "/")+1:]
		if imp == "fmt" || strings.Contains(body.String(), last+".") {
			fmt.Fprintf(&b, "\t%q\n", imp)
		}
	}
	b.WriteString(")\n\n")
	for _, t := range d.Types {
		if t.Declared {
			fmt.Fprintf(&b, "type %s %s\n", t.Name, t.Under)
		}
	}
	b.WriteString("\nvar (\n")
	for _, t := range d.Types {
		for i, v := range t.Values {
			fmt.Fprintf(&b, "\t%s %s = %s\n", varName(t, i), t.Name, v)
		}
	}
	b.WriteString(")\n\n")
	b.WriteString(`func try(i int, f func() any) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%d: panic: %v\n", i, r)
		}
	}()
	v := f()
	fmt.Printf("%d: %T %v\n", i, v, v)
}

func id[T any](x T) T { return x }

func main() {
`)
	b.WriteString(body.String())
	b.WriteString("}\n")
	return b.String()
}

// Shrink returns single-step reductions of p, smallest first: every
// same-typed descendant hoisted into the root position, every subtree
// replaced by a same-typed leaf, and every variable swapped for the
// "simplest" value of its type.
func Shrink(p Probe) []Probe {
	var out []Probe
	seen := map[string]bool{p.Body(): true}
	add := func(n *Node, ctx string) {
		q := Probe{D: p.D, Ctx: ctx, Root: n}
		if !q.Valid() {
			return
		}
		if k := q.Body(); !seen[k] {
			seen[k] = true
			out = append(out, q)
		}
	}
	if p.Ctx != "expr" {
		add(p.Root, "expr")
	}
	// the probe returns `any`, so in a plain expression context the root
	// may become a descendant of any type — this is what lets a wrong
	// value surface at the smallest subexpression that computes it.
	if p.Ctx == "expr" {
		for _, d := range descendants(p.Root) {
			if d.Op != "var" {
				add(d, "expr")
			}
		}
	}
	var walk func(n *Node, rebuild func(*Node) *Node)
	walk = func(n *Node, rebuild func(*Node) *Node) {
		// hoist same-typed descendants
		for _, d := range descendants(n) {
			if d.T == n.T {
				add(rebuild(d), p.Ctx)
			}
		}
		if n.Op == "var" {
			for i := range n.T.Values {
				add(rebuild(&Node{T: n.T, Op: "var", Text: varName(n.T, i)}), p.Ctx)
			}
			return
		}
		if n.Op != "lit" {
			add(rebuild(&Node{T: n.T, Op: "var", Text: varName(n.T, 0)}), p.Ctx)
		}
		for i, k := range n.Kids {
			walk(k, func(r *Node) *Node {
				c := *n
				c.Kids = append([]*Node{}, n.Kids...)
				c.Kids[i] = r
				return rebuild(&c)
			})
		}
	}
	walk(p.Root, func(r *Node) *Node { return r })
	sort.SliceStable(out, func(i, j int) bool { return out[i].Size() < out[j].Size() })
	return out
}

func descendants(n *Node) []*Node {
	var out []*Node
	for _, k := range n.Kids {
		out = append(out, k)
		out = append(out, descendants(k)...)
	}
	return out
}
