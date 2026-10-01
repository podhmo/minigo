// Package main is a minigo script that infers net/http request
// parameters (OpenAPI-style: query/path/header/cookie/form/body) by
// walking the compiled body view of handlers — inspect.Ops: a flat
// list of ref/sel/call/bind/ret ops ("special VM code") that lets a
// script track which values flow into a call as arguments and back
// out as results. It exists to answer: what does inspect need for a
// script to touch a function's body?
//
// The walk descends into helper functions — the interesting read may
// hide inside them — by resolving each call's callee to its decl and
// re-entering with the callee's parameter names bound to the caller's
// tracked values. It stops where lookup finds no decl (the standard
// library has none — shallow by design).
package main

import (
	"sort"
	"strings"

	"github.com/podhmo/minigo/inspect"
)

const requestType = "*net/http.Request"

// ---- scan state ----

// scope is the per-function tracking context: the op list being
// scanned plus the value-class maps. A class is "" (untracked), "req"
// (the *http.Request itself), or an accessor kind derived from it —
// "query" (r.URL.Query()), "header" (r.Header), "form" (r.Form), or
// "dec" (a json.Decoder built from r.Body).
type scope struct {
	ops    []any
	req    map[string]bool
	ali    map[string]string // bound name -> class
	vt     map[string]int    // local var name -> vte index
	vte    []any             // *inspect.TypeExpr of declared local types
	regc   map[int]string    // register -> class (memoized)
	argOf  map[int]int       // register -> consuming call op's Dst
	retReq bool              // a ret op returns a req-classed value
}

var cur scope
var stack []scope

var params []string
var found map[string]bool
var seen map[string]bool
var routes map[string]string
var litRows []string
var mode string // "routes" | "params"

func newScope(ops []any) scope {
	return scope{
		ops:   ops,
		req:   map[string]bool{},
		ali:   map[string]string{},
		vt:    map[string]int{},
		regc:  map[int]string{},
		argOf: map[int]int{},
	}
}

func newScan() {
	cur = newScope(nil)
	stack = nil
	params = []string{}
	found = map[string]bool{}
}

func pushScope(ops []any) {
	stack = append(stack, cur)
	cur = newScope(ops)
}

func popScope() {
	cur = stack[len(stack)-1]
	stack = stack[:len(stack)-1]
}

func opsList(x any) []any {
	var out []any
	for _, o := range inspect.Ops(x) {
		out = append(out, o)
	}
	return out
}

func op(i int) any {
	if i < 0 || i >= len(cur.ops) {
		return nil
	}
	return cur.ops[i]
}

// ---- value classes ----

func refClass(name string) string {
	if cur.req[name] {
		return "req"
	}
	if k, ok := cur.ali[name]; ok {
		return k
	}
	return ""
}

// regClass computes a register's derivation class lazily; results are
// memoized per scope. Reading it never emits or descends.
func regClass(r int) string {
	o := op(r)
	if o == nil {
		return ""
	}
	if k, ok := cur.regc[o.Dst]; ok {
		return k
	}
	k := ""
	switch o.Kind {
	case "ref":
		k = refClass(o.Name)
	case "sel", "index":
		k = regClass(o.X)
	case "call":
		k = callResultClass(o)
	}
	cur.regc[o.Dst] = k
	return k
}

// callResultClass classifies a call's result: accessor calls
// propagate a derivation class (r.URL.Query() is "query"), a
// json.NewDecoder over the request is "dec"; everything else is
// untracked.
func callResultClass(o any) string {
	rc, mem, _ := chainInfo(o.Fun)
	if rc == "req" && len(mem) > 0 {
		switch mem[0] {
		case "Query":
			return "query"
		case "Header":
			return "header"
		case "Form", "PostForm":
			return "form"
		}
	}
	if sid := inspect.SymbolID(o); sid != nil {
		if sid.PackagePath == "encoding/json" && sid.Name == "NewDecoder" {
			if len(o.Args) > 0 && regClass(int(o.Args[0])) == "req" {
				return "dec"
			}
		}
	}
	return ""
}

// chainInfo walks a call's callee register back through sels/calls to
// its root, returning the root's class, the member path applied from
// the root (outermost member first), and the root's ref name.
func chainInfo(r int) (string, []string, string) {
	var mem []string
	for {
		o := op(r)
		if o == nil {
			return "", mem, ""
		}
		switch o.Kind {
		case "sel":
			mem = append(mem, o.Name)
			r = o.X
		case "index":
			r = o.X
		case "call":
			return regClass(r), mem, ""
		case "ref":
			return refClass(o.Name), mem, o.Name
		default:
			return "", mem, ""
		}
	}
}

func memEq(mem []string, pat ...string) bool {
	if len(mem) != len(pat) {
		return false
	}
	for i, p := range pat {
		if mem[i] != p {
			return false
		}
	}
	return true
}

func unq(v string) string {
	if len(v) >= 2 && v[0:1] == "\"" {
		return v[1 : len(v)-1]
	}
	return v
}

func litArg(o any, i int) string {
	if len(o.Args) <= i {
		return ""
	}
	a := op(int(o.Args[i]))
	if a != nil && a.Kind == "lit" && a.Tok == "STRING" {
		return unq(a.Value)
	}
	return ""
}

func typeText(te any) string {
	if te == nil {
		return ""
	}
	c := te.CanonicalName()
	if c != "" {
		return c
	}
	return te.Text
}

func emit(where, name, typ string) {
	key := where + " " + name
	if found[key] {
		return
	}
	found[key] = true
	row := where + " " + name
	if typ != "" {
		row += " " + typ
	}
	params = append(params, row)
}

// ---- op handling ----

func scanOps() {
	// index which register each call consumes as an argument (so an
	// inner read re-typed by an enclosing conversion isn't emitted
	// twice — the wrapper emits it with the richer type).
	for _, o := range cur.ops {
		if o.Kind == "call" {
			for _, a := range o.Args {
				cur.argOf[int(a)] = o.Dst
			}
		}
	}
	for _, o := range cur.ops {
		switch o.Kind {
		case "bind":
			noteBind(o)
		case "call":
			noteCall(o)
		case "ret":
			for _, s := range o.Srcs {
				if regClass(int(s)) == "req" {
					cur.retReq = true
				}
			}
		}
	}
}

func noteBind(o any) {
	if len(o.Srcs) == 0 {
		// parameter or `var x T`: the declared type is on the op.
		te := inspect.AsExpr(o)
		if te == nil {
			return
		}
		for _, name := range o.Names {
			cur.vt[name] = len(cur.vte)
			cur.vte = append(cur.vte, te)
			if te.CanonicalName() == requestType {
				cur.req[name] = true
			}
		}
		return
	}
	for i, name := range o.Names {
		src := int(o.Srcs[0])
		if i < len(o.Srcs) {
			src = int(o.Srcs[i])
		}
		if k := regClass(src); k != "" {
			cur.ali[name] = k
		}
		if so := op(src); so != nil && so.Kind == "comp" {
			if te := inspect.AsExpr(so); te != nil {
				cur.vt[name] = len(cur.vte)
				cur.vte = append(cur.vte, te)
			}
		}
	}
}

var convTypes = map[string]string{
	"strconv.Atoi":       "int64",
	"strconv.ParseInt":   "int64",
	"strconv.ParseBool":  "bool",
	"strconv.ParseFloat": "float64",
}

func isConv(o any) bool {
	sid := inspect.SymbolID(o)
	if sid == nil {
		return false
	}
	_, ok := convTypes[sid.PackagePath+"."+sid.Name]
	return ok
}

func noteCall(o any) {
	if mode == "routes" {
		noteRoute(o)
		return
	}
	if isConv(o) {
		// conversion wrapper: re-type the inner read.
		for _, a := range o.Args {
			io := op(int(a))
			if io != nil && io.Kind == "call" {
				noteParam(io, convTypes[symKey(o)])
			}
		}
	} else if p, ok := cur.argOf[o.Dst]; ok && isConv(op(p)) {
		// this read's result feeds a conversion — the wrapper emits
		// it with the richer type.
	} else {
		noteParam(o, "")
	}
	// memoize this call's own class, then try to descend.
	if regClass(o.Dst) == "" && maybeDescend(o) {
		// the callee returns a request-classed value — the result
		// keeps tracking (call argument and return tracking, both).
		cur.regc[o.Dst] = "req"
	}
}

func symKey(o any) string {
	sid := inspect.SymbolID(o)
	if sid == nil {
		return ""
	}
	return sid.PackagePath + "." + sid.Name
}

func noteParam(o any, typ string) {
	rc, mem, _ := chainInfo(o.Fun)
	if typ == "" {
		typ = "string"
	}
	arg0 := litArg(o, 0)
	switch {
	case rc == "req" && memEq(mem, "PathValue"):
		emit("path", arg0, "string")
	case rc == "req" && memEq(mem, "Cookie"):
		emit("cookie", arg0, "string")
	case rc == "req" && (memEq(mem, "FormValue") || memEq(mem, "PostFormValue")):
		emit("form", arg0, "string")
	case rc == "req" && memEq(mem, "Get", "Header"):
		emit("header", arg0, "string")
	case rc == "req" && (memEq(mem, "Get", "Form") || memEq(mem, "Get", "PostForm")):
		emit("form", arg0, "string")
	case rc == "query" && memEq(mem, "Get"):
		emit("query", arg0, typ)
	case rc == "query" && memEq(mem, "Has"):
		emit("query", arg0, "bool")
	case rc == "header" && memEq(mem, "Get"):
		emit("header", arg0, typ)
	case rc == "form" && memEq(mem, "Get"):
		emit("form", arg0, typ)
	case rc == "dec" && memEq(mem, "Decode"):
		emitBody(o.Args)
	}
}

// emitBody renders the JSON body schema of json.NewDecoder(r.Body)
// .Decode(&v): the decoder's argument is request-rooted (class "dec")
// and Decode's argument names the body struct type.
func emitBody(args any) {
	if len(args) == 0 {
		return
	}
	a := op(int(args[0]))
	if a == nil {
		emit("body", "?", "")
		return
	}
	var te any
	switch a.Kind {
	case "ref":
		if i, ok := cur.vt[a.Name]; ok {
			te = cur.vte[i]
		}
	case "comp":
		te = inspect.AsExpr(a)
	}
	if te == nil {
		emit("body", "?", "")
		return
	}
	d := inspect.Lookup(te)
	if d == nil || d.Kind != "type" {
		emit("body", "?", typeText(te))
		return
	}
	def := inspect.Def(d)
	if def == nil || def.Kind != "StructType" {
		emit("body", "?", typeText(te))
		return
	}
	for _, f := range inspect.Fields(d) {
		if f.Embedded {
			continue
		}
		name := jsonName(f.Tag)
		if name == "-" {
			continue
		}
		if name == "" && len(f.Names) > 0 {
			name = f.Names[0]
		}
		if name != "" {
			emit("body", name, typeText(f.Type))
		}
	}
}

func jsonName(tag string) string {
	i := strings.Index(tag, "json:")
	if i < 0 {
		return ""
	}
	v := tag[i+5:]
	if len(v) < 2 || v[0:1] != "\"" {
		return ""
	}
	j := strings.Index(v[1:], "\"")
	if j < 0 {
		return ""
	}
	inner := v[1 : j+1]
	if k := strings.Index(inner, ","); k >= 0 {
		inner = inner[:k]
	}
	return inner
}

// maybeDescend resolves a call's callee to its decl and reports
// whether the callee returns a request-classed value. Lookup reports
// nil for anything without source (stdlib funcs have no decl — the
// walk stops there by design).
func maybeDescend(o any) bool {
	fun := op(o.Fun)
	if fun == nil {
		return false
	}
	var d any
	switch fun.Kind {
	case "ref":
		// a bare name can be a local var or a decl — check locals
		// first, then Lookup.
		if cur.req[fun.Name] {
			return false
		}
		if _, ok := cur.ali[fun.Name]; ok {
			return false
		}
		if _, ok := cur.vt[fun.Name]; ok {
			return false
		}
		d = inspect.Lookup(fun)
	case "sel":
		d = inspect.Lookup(fun)
		if d == nil {
			// local variable's method: srv.GetItem
			b := op(fun.X)
			if b != nil && b.Kind == "ref" {
				if i, ok := cur.vt[b.Name]; ok {
					td := inspect.Lookup(inspect.UnRef(cur.vte[i]))
					if td != nil && td.Kind == "type" {
						for _, m := range inspect.Methods(td) {
							if m.Name == fun.Name {
								d = m
							}
						}
					}
				}
			}
		}
	default:
		return false
	}
	if d == nil || (d.Kind != "func" && d.Kind != "method") {
		return false
	}
	return descend(d, o)
}

// descend continues the scan inside the callee's ops: a request-
// classed call argument binds the callee's parameter name to the same
// class. It reports whether the callee's ret ops hand the request
// back out as a return value.
func descend(d any, callOp any) bool {
	key := d.Pos + " " + d.Name
	if seen[key] {
		return false
	}
	cops := inspect.Ops(d)
	if cops == nil {
		return false
	}
	// map the caller's tracked arguments onto the callee's leading
	// "param" binds.
	marks := map[string]string{}
	pi := 0
	for _, cp := range cops {
		if cp.Kind != "bind" || cp.Tok != "param" {
			break
		}
		if pi < len(callOp.Args) {
			if k := regClass(int(callOp.Args[pi])); k != "" {
				for _, n := range cp.Names {
					marks[n] = k
				}
			}
		}
		pi++
	}
	if len(marks) == 0 {
		return false
	}
	seen[key] = true
	pushScope(opsList(d))
	for n, k := range marks {
		if k == "req" {
			cur.req[n] = true
		} else {
			cur.ali[n] = k
		}
	}
	scanOps()
	rr := cur.retReq
	popScope()
	return rr
}

// ---- routes ----

// noteRoute binds HandleFunc/Handle registrations to handler names.
func noteRoute(o any) {
	fun := op(o.Fun)
	if fun == nil || fun.Kind != "sel" {
		return
	}
	if fun.Name != "HandleFunc" && fun.Name != "Handle" {
		return
	}
	if len(o.Args) < 2 {
		return
	}
	a0 := op(int(o.Args[0]))
	if a0 == nil || a0.Kind != "lit" || a0.Tok != "STRING" {
		return
	}
	pat := unq(a0.Value)
	a1 := op(int(o.Args[1]))
	if a1 == nil {
		return
	}
	switch a1.Kind {
	case "func":
		reportLit(a1, pat)
	case "ref", "sel":
		routes[a1.Name] = pat
	}
}

// reportLit scans an anonymous handler where it is registered: its
// own leading param binds mark the request name automatically.
func reportLit(lit any, pat string) {
	saveCur, saveMode := cur, mode
	saveParams, saveFound, saveSeen, saveStack := params, found, seen, stack
	newScan()
	mode = "params"
	seen = map[string]bool{}
	cur = newScope(opsList(lit))
	scanOps()
	sort.Strings(params)
	row := "handler " + pat + " (func literal)"
	for _, p := range params {
		row += "\n  " + p
	}
	litRows = append(litRows, row)
	cur, mode = saveCur, saveMode
	params, found, seen, stack = saveParams, saveFound, saveSeen, saveStack
}

func collectRoutes() {
	routes = map[string]string{}
	p := inspect.DirOf("./testdata/httppkg")
	for _, d := range inspect.Decls(p) {
		if d.Kind != "func" && d.Kind != "method" {
			continue
		}
		mode = "routes"
		newScan()
		cur = newScope(opsList(d))
		scanOps()
	}
}

// ---- handlers ----

// reqNameOf returns the name of a decl's *http.Request parameter, or
// "" when the decl is not a handler shape.
func reqNameOf(d any) string {
	sig := inspect.Signature(d)
	if sig == nil {
		return ""
	}
	for _, f := range sig.Params {
		if f.Type == nil || len(f.Names) == 0 {
			continue
		}
		if f.Type.CanonicalName() == requestType {
			return f.Names[0]
		}
	}
	return ""
}

func reportHandler(d any) string {
	mode = "params"
	seen = map[string]bool{}
	newScan()
	cur = newScope(opsList(d))
	scanOps()
	sort.Strings(params)
	out := "handler " + d.Name
	if r, ok := routes[d.Name]; ok {
		out += " " + r
	}
	for _, p := range params {
		out += "\n  " + p
	}
	return out
}

// ---- entry points (driven by Go tests via e.Run) ----

// Report renders the inferred parameter table as canonical text.
func Report() string {
	litRows = nil
	collectRoutes()
	var rows []string
	p := inspect.DirOf("./testdata/httppkg")
	for _, d := range inspect.Decls(p) {
		if d.Kind == "func" && reqNameOf(d) != "" {
			rows = append(rows, reportHandler(d))
		}
		if d.Kind == "type" {
			for _, m := range inspect.Methods(d) {
				if reqNameOf(m) != "" {
					rows = append(rows, reportHandler(m))
				}
			}
		}
	}
	rows = append(rows, litRows...)
	sort.Strings(rows)
	return strings.Join(rows, "\n")
}

// BodySmoke is a smoke test for the generic Node body view: a func
// decl's BlockStmt root, children, and printed text.
func BodySmoke() string {
	p := inspect.DirOf("./testdata/httppkg")
	d := inspect.Symbol(p, "GetUser")
	b := inspect.Body(d)
	if b == nil {
		return "no body"
	}
	if b.Kind != "BlockStmt" {
		return "not a block: " + b.Kind
	}
	if len(inspect.Nodes(b)) == 0 {
		return "empty body"
	}
	if !strings.Contains(b.Text, "PathValue") {
		return "text missing: " + b.Text
	}
	return "ok"
}

// OpsSmoke is a smoke test for the compiled op view: GetUser compiles
// into leading param binds followed by body ops, and call args carry
// registers.
func OpsSmoke() string {
	p := inspect.DirOf("./testdata/httppkg")
	d := inspect.Symbol(p, "GetUser")
	ops := opsList(d)
	if len(ops) == 0 {
		return "no ops"
	}
	first := ops[0]
	if first.Kind != "bind" || first.Tok != "param" {
		return "first op: " + first.Kind + " " + first.Tok
	}
	ncall := 0
	nret := 0
	for _, o := range ops {
		if o.Kind == "call" && len(o.Args) > 0 {
			ncall++
		}
		if o.Kind == "ret" {
			nret++
		}
	}
	if ncall == 0 {
		return "no call args"
	}
	return "ok"
}

func main() {}
