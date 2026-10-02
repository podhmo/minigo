// Package httpinspect experiments with bounded abstract interpretation over
// inspect syntax views. It never executes the analyzed program or its imports.
package httpinspect

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/inspect"
)

// Parameter is an OpenAPI parameter candidate, not a validated API contract.
type Parameter struct {
	Name     string   `json:"name"`
	In       string   `json:"in"`
	Required bool     `json:"required"`
	Schema   Schema   `json:"schema"`
	Evidence []string `json:"evidence"`
}
type Schema struct {
	Type string `json:"type"`
}
type Diagnostic struct {
	Pos    string `json:"pos"`
	Reason string `json:"reason"`
}
type Trace struct {
	Target   string   `json:"target"`
	Args     []string `json:"args"`
	Results  []string `json:"results"`
	Boundary bool     `json:"boundary"`
	Captures []string `json:"captures,omitempty"`
}
type Report struct {
	Parameters  []Parameter  `json:"parameters"`
	Diagnostics []Diagnostic `json:"diagnostics"`
	Calls       []Trace      `json:"calls"`
	Incomplete  bool         `json:"incomplete"`
	Steps       int          `json:"steps"`
}
type Limits struct {
	Depth int
	Steps int
}

type atom struct {
	kind, text, origin string
	fn                 *callable
	object             *sourceObject
}
type value []atom

// Names are lexical bindings; the store is copied per abstract path. Callable
// captures refer to binding IDs rather than an environment snapshot.
type env struct {
	names map[string]string
	local map[string]bool
	store map[string]value
}
type flow struct {
	vars     env
	returned []value
	done     bool
}
type frame struct {
	decl    *inspect.Decl
	imports map[string]string
	depth   int
	results []parameter
}
type analyzer struct {
	engine    *minigo.Engine
	ctx       context.Context
	limits    Limits
	report    Report
	params    map[string]*Parameter
	diagnosed map[string]bool
	exhausted bool
	nextID    int
}

// Analyze follows source helpers on demand and substitutes summaries for known
// request APIs. Unsupported operations make the report explicitly incomplete.
func Analyze(ctx context.Context, e *minigo.Engine, decl *inspect.Decl, limits Limits) (Report, error) {
	if limits.Depth <= 0 {
		limits.Depth = 8
	}
	if limits.Steps <= 0 {
		limits.Steps = 2000
	}
	if _, err := inspect.BodyOf(decl); err != nil {
		return Report{}, err
	}
	sig, err := inspect.SignatureOf(decl)
	if err != nil {
		return Report{}, err
	}
	var args []value
	request := false
	for _, field := range sig.ParamFields() {
		v := unknown()
		if sid, ok := field.Type.Unref().SymbolID(); ok && field.Type.Kind == "StarExpr" && sid.PackagePath == "net/http" && sid.Name == "Request" {
			v = scalar("request", "")
			request = true
		}
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		for i := 0; i < count; i++ {
			args = append(args, v)
		}
	}
	if !request {
		return Report{}, fmt.Errorf("entry %s needs an explicit *net/http.Request parameter", decl.Name)
	}
	a := &analyzer{engine: e, ctx: ctx, limits: limits, params: map[string]*Parameter{}, diagnosed: map[string]bool{}}
	a.invoke(sourceFunction(decl), args, 0, nil, map[string]value{})
	for _, p := range a.params {
		sort.Strings(p.Evidence)
		a.report.Parameters = append(a.report.Parameters, *p)
	}
	sort.Slice(a.report.Parameters, func(i, j int) bool {
		x, y := a.report.Parameters[i], a.report.Parameters[j]
		return x.In+":"+x.Name < y.In+":"+y.Name
	})
	a.report.Incomplete = len(a.report.Diagnostics) != 0
	return a.report, nil
}
func scalar(kind, text string) value { return value{{kind: kind, text: text}} }
func unknown() value                 { return scalar("unknown", "") }
func copyNames(xs map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range xs {
		out[k] = v
	}
	return out
}
func clone(v env) env {
	out := env{names: copyNames(v.names), local: map[string]bool{}, store: map[string]value{}}
	for k, x := range v.local {
		out.local[k] = x
	}
	for k, x := range v.store {
		out.store[k] = x
	}
	return out
}
func (v env) lookup(name string) (value, bool) {
	id, ok := v.names[name]
	if !ok {
		return nil, false
	}
	x, ok := v.store[id]
	if !ok {
		return unknown(), true
	}
	return x, true
}
func (a *analyzer) allocate() string { a.nextID++; return fmt.Sprintf("b%d", a.nextID) }
func (a *analyzer) bind(v env, name string, x value, declare bool) {
	if name == "_" || name == "" {
		return
	}
	id, exists := v.names[name]
	if declare && !v.local[name] || !exists {
		id = a.allocate()
		v.names[name] = id
		v.local[name] = true
	}
	v.store[id] = a.copyValue(x, v.store)
}
func union(a, b value) value {
	out := append(value(nil), a...)
	for _, x := range b {
		found := false
		for _, y := range out {
			if x == y {
				found = true
				break
			}
		}
		if !found {
			out = append(out, x)
		}
	}
	return out
}
func describe(v value) string {
	var xs []string
	for _, x := range v {
		s := x.kind
		if x.fn != nil {
			s = functionDescription(x)
		}
		if x.object != nil {
			s += ":" + objectDescription(x)
		}
		if x.text != "" && x.object == nil {
			s += ":" + x.text
		}
		if x.origin != "" {
			s += "@" + x.origin
		}
		xs = append(xs, s)
	}
	sort.Strings(xs)
	return strings.Join(xs, "|")
}
func describeAll(vs []value) []string {
	var out []string
	for _, v := range vs {
		out = append(out, describe(v))
	}
	return out
}
func children(n *inspect.Node, role string) []*inspect.Node {
	var out []*inspect.Node
	if n != nil {
		for _, c := range n.ChildNodes() {
			if c.Role == role {
				out = append(out, c)
			}
		}
	}
	return out
}
func child(n *inspect.Node, role string) *inspect.Node {
	xs := children(n, role)
	if len(xs) > 0 {
		return xs[0]
	}
	return nil
}
func (a *analyzer) diagnostic(n *inspect.Node, reason string) {
	pos := ""
	if n != nil {
		pos = n.Pos
	}
	key := pos + reason
	if !a.diagnosed[key] {
		a.diagnosed[key] = true
		a.report.Diagnostics = append(a.report.Diagnostics, Diagnostic{Pos: pos, Reason: reason})
	}
}
func (a *analyzer) tick(n *inspect.Node) bool {
	if a.exhausted {
		return false
	}
	if a.ctx.Err() != nil {
		a.exhausted = true
		a.diagnostic(n, a.ctx.Err().Error())
		return false
	}
	if a.report.Steps >= a.limits.Steps {
		a.exhausted = true
		a.diagnostic(n, "step budget exhausted")
		return false
	}
	a.report.Steps++
	return true
}
func (a *analyzer) invoke(fn *callable, args []value, depth int, site *inspect.Node, store map[string]value) []value {
	if depth >= a.limits.Depth {
		a.diagnostic(site, "call depth limit: "+fn.name)
		return []value{unknown()}
	}
	if fn.body == nil {
		a.diagnostic(site, "source body unavailable: "+fn.name)
		return []value{unknown()}
	}
	f := frame{decl: fn.owner, depth: depth, imports: importsOf(fn.owner), results: fn.results}
	vars := env{names: copyNames(fn.captures), local: map[string]bool{}, store: store}
	var captures []string
	for name, id := range fn.captures {
		captures = append(captures, name+"="+describe(store[id]))
	}
	sort.Strings(captures)
	if fn.receiver != nil {
		a.bind(vars, fn.receiverName, fn.receiver, true)
	}
	if fn.variadic {
		a.diagnostic(site, "variadic function binding unsupported")
	}
	if fn.generic {
		a.diagnostic(site, "generic function binding unsupported")
	}
	if len(args) != len(fn.params) {
		a.diagnostic(site, "argument count mismatch: "+fn.name)
	}
	for i, param := range fn.params {
		v := unknown()
		if i < len(args) {
			v = args[i]
		}
		a.bind(vars, param.name, v, true)
	}
	for _, param := range fn.results {
		a.bind(vars, param.name, unknown(), true)
	}
	flows := a.block(fn.body, []flow{{vars: vars}}, f)
	var result []value
	joined := map[string]value{}
	for _, p := range flows {
		for id, v := range p.vars.store {
			joined[id] = union(joined[id], v)
		}
		rs := p.returned
		if !p.done && len(fn.results) > 0 {
			a.diagnostic(fn.body, "result fallthrough is unsupported")
			for range fn.results {
				rs = append(rs, unknown())
			}
		}
		for j, v := range rs {
			for len(result) <= j {
				result = append(result, nil)
			}
			result[j] = union(result[j], v)
		}
	}
	// Copy the joined store back so captured writes and escaped local bindings
	// survive the call, without sharing branch stores with other paths.
	for id := range store {
		delete(store, id)
	}
	for id, v := range joined {
		store[id] = v
	}
	a.report.Calls = append(a.report.Calls, Trace{Target: fn.name, Args: describeAll(args), Results: describeAll(result), Captures: captures})
	return result
}
func (a *analyzer) block(n *inspect.Node, paths []flow, f frame) []flow {
	for _, s := range children(n, "List") {
		var next []flow
		for _, p := range paths {
			if p.done || a.exhausted {
				next = append(next, p)
			} else {
				next = append(next, a.stmt(s, p, f)...)
			}
		}
		paths = next
	}
	return paths
}
func (a *analyzer) stmt(n *inspect.Node, p flow, f frame) []flow {
	if !a.tick(n) {
		return []flow{p}
	}
	switch n.Kind {
	case "AssignStmt":
		lhs, rhs := children(n, "Lhs"), children(n, "Rhs")
		slots := make([][]string, len(lhs))
		for i, l := range lhs {
			if l.Kind == "SelectorExpr" {
				slots[i] = a.fieldSlots(l, p.vars, f)
			}
		}
		var vals []value
		for _, r := range rhs {
			vals = append(vals, a.expr(r, p.vars, f)...)
		}
		// Pointer reads on the left may be evaluated before or after an RHS
		// call. Keep both destinations when a pure receiver read changed.
		for i, l := range lhs {
			if l.Kind == "SelectorExpr" && canReRead(child(l, "X")) {
				later := a.fieldSlots(l, p.vars, f)
				if !sameSlots(slots[i], later) {
					a.diagnostic(l, "evaluation order may change assignment destination")
					slots[i] = mergeSlots(slots[i], later)
				}
			}
		}
		// Evaluate all RHS expressions before writing any LHS slot.
		for i, l := range lhs {
			if l.Kind == "SelectorExpr" {
				v := unknown()
				if i < len(vals) && n.Token == "=" {
					v = vals[i]
				}
				if n.Token != "=" {
					a.diagnostic(n, "compound field assignment unsupported")
				}
				for _, id := range slots[i] {
					next := a.copyValue(v, p.vars.store)
					if len(slots[i]) > 1 {
						next = union(p.vars.store[id], next)
					}
					p.vars.store[id] = next
				}
				continue
			}
			if l.Kind != "Ident" {
				a.diagnostic(l, "assignment target unsupported")
				continue
			}
			if l.Text == "_" {
				continue
			}
			v := unknown()
			if i < len(vals) {
				v = vals[i]
			}
			if n.Token != "=" && n.Token != ":=" {
				v = unknown()
				a.diagnostic(n, "compound assignment unsupported")
			}
			a.bind(p.vars, l.Text, v, n.Token == ":=")
		}
	case "DeclStmt":
		for _, spec := range children(child(n, "Decl"), "Specs") {
			if spec.Kind != "ValueSpec" {
				a.diagnostic(spec, "local declaration unsupported")
				continue
			}
			var vals []value
			for _, r := range children(spec, "Values") {
				vals = append(vals, a.expr(r, p.vars, f)...)
			}
			for i, name := range children(spec, "Names") {
				v := unknown()
				if i < len(vals) {
					v = vals[i]
				}
				a.bind(p.vars, name.Text, v, true)
			}
		}
	case "ExprStmt":
		a.expr(child(n, "X"), p.vars, f)
	case "ReturnStmt":
		p.done = true
		for _, r := range children(n, "Results") {
			p.returned = append(p.returned, a.expr(r, p.vars, f)...)
		}
		if len(p.returned) == 0 {
			for _, param := range f.results {
				v, _ := p.vars.lookup(param.name)
				if v == nil {
					v = unknown()
				}
				p.returned = append(p.returned, v)
			}
		}
	case "IfStmt":
		outer := p.vars
		local := env{names: copyNames(outer.names), local: map[string]bool{}, store: outer.store}
		if init := child(n, "Init"); init != nil {
			a.stmt(init, flow{vars: local}, f)
		}
		a.expr(child(n, "Cond"), local, f)
		then := a.scopedBlock(child(n, "Body"), flow{vars: clone(local)}, f)
		other := []flow{{vars: clone(local)}}
		if els := child(n, "Else"); els != nil {
			if els.Kind == "BlockStmt" {
				other = a.scopedBlock(els, other[0], f)
			} else {
				other = a.stmt(els, other[0], f)
			}
		}
		all := append(then, other...)
		for i := range all {
			all[i].vars.names = copyNames(outer.names)
			all[i].vars.local = outer.local
		}
		return all
	case "BlockStmt":
		return a.scopedBlock(n, p, f)
	case "EmptyStmt":
	default:
		a.diagnostic(n, "statement unsupported: "+n.Kind)
		for id := range p.vars.store {
			p.vars.store[id] = unknown()
		}
		// Do not inspect an unsupported subtree with fabricated execution semantics.
	}
	return []flow{p}
}
func (a *analyzer) scopedBlock(n *inspect.Node, p flow, f frame) []flow {
	outer := p.vars
	p.vars = env{names: copyNames(outer.names), local: map[string]bool{}, store: outer.store}
	out := a.block(n, []flow{p}, f)
	for i := range out {
		out[i].vars.names = copyNames(outer.names)
		out[i].vars.local = outer.local
	}
	return out
}
func (a *analyzer) expr(n *inspect.Node, vars env, f frame) []value {
	if n == nil || !a.tick(n) {
		return []value{unknown()}
	}
	switch n.Kind {
	case "Ident":
		if v, ok := vars.lookup(n.Text); ok {
			return []value{v}
		}
		if d := f.decl.Package.Index.Funcs[n.Text]; d != nil {
			return []value{{atom{kind: "function", fn: sourceFunction(inspect.NewDecl(f.decl.Package, d))}}}
		}
		if td := f.decl.Package.Index.Types[n.Text]; td != nil {
			d := inspect.NewDecl(f.decl.Package, td.Decl)
			return []value{{{kind: "type", text: d.Package.Path + "." + d.Name, object: &sourceObject{typ: d}}}}
		}
		return []value{unknown()}
	case "BasicLit":
		if n.Token == "STRING" {
			s, err := strconv.Unquote(n.Text)
			if err == nil {
				return []value{scalar("literal", s)}
			}
		}
		return []value{unknown()}
	case "ParenExpr":
		return a.expr(child(n, "X"), vars, f)
	case "SelectorExpr":
		return []value{a.selectValue(n, vars, f)}
	case "FuncLit":
		return []value{a.literal(n, vars)}
	case "CompositeLit":
		return []value{a.composite(n, vars, f)}
	case "StarExpr":
		xs := dereferenceObjects(a.expr(child(n, "X"), vars, f)[0], vars.store)
		var out value
		for _, x := range xs {
			if x.object == nil {
				a.diagnostic(n, "dereference operand unsupported")
				out = union(out, unknown())
				continue
			}
			obj := *x.object
			obj.pointer = x.kind == "type"
			if x.kind != "type" {
				obj.binding = ""
			}
			x.object = &obj
			out = union(out, value{x})
		}
		return []value{out}
	case "UnaryExpr":
		if n.Token == "&" {
			return []value{a.address(n, vars, f)}
		}
		a.diagnostic(n, "unary expression unsupported: "+n.Token)
		return []value{unknown()}
	case "CallExpr":
		return a.call(n, vars, f)
	case "BinaryExpr":
		a.expr(child(n, "X"), vars, f)
		a.expr(child(n, "Y"), vars, f)
		a.diagnostic(n, "binary expression has unknown value: "+n.Token)
		return []value{unknown()}
	default:
		a.diagnostic(n, "expression unsupported: "+n.Kind)
		return []value{unknown()}
	}
}
func (a *analyzer) parameter(n *inspect.Node, in string, key value) value {
	var out value
	for _, k := range key {
		if k.kind != "literal" {
			a.diagnostic(n, "dynamic "+in+" parameter name")
			out = union(out, unknown())
			continue
		}
		id := in + ":" + k.text
		p := a.params[id]
		if p == nil {
			p = &Parameter{Name: k.text, In: in, Required: in == "path", Schema: Schema{Type: "string"}}
			a.params[id] = p
		}
		a.evidence(p, n.Pos)
		out = union(out, value{{kind: "parameter", origin: id}})
	}
	return out
}
func (a *analyzer) evidence(p *Parameter, s string) {
	for _, x := range p.Evidence {
		if x == s {
			return
		}
	}
	p.Evidence = append(p.Evidence, s)
}
func (a *analyzer) boundary(target string, args []value, results []value) []value {
	a.report.Calls = append(a.report.Calls, Trace{Target: target, Args: describeAll(args), Results: describeAll(results), Boundary: true})
	return results
}
func (a *analyzer) call(n *inspect.Node, vars env, f frame) []value {
	if n.Token == "..." {
		a.diagnostic(n, "variadic call expansion unsupported")
	}
	// Calls are sequenced lexically, but plain receiver/argument reads can
	// move relative to neighboring calls. Start with one traversal order,
	// then retain alternative pure reads if those calls changed their value.
	fun := child(n, "Fun")
	targets := a.expr(fun, vars, f)[0]
	var args []value
	type argSpan struct {
		node       *inspect.Node
		start, end int
	}
	var spans []argSpan
	for _, arg := range children(n, "Args") {
		start := len(args)
		for _, v := range a.expr(arg, vars, f) {
			args = append(args, a.copyValue(v, vars.store))
		}
		spans = append(spans, argSpan{arg, start, len(args)})
	}
	if canReRead(fun) {
		later := a.expr(fun, vars, f)[0]
		if semanticKey(targets, vars.store) != semanticKey(later, vars.store) {
			a.diagnostic(n, "evaluation order may change call target or receiver")
			targets = union(targets, later)
		}
	}
	for _, span := range spans {
		if !canReRead(span.node) {
			continue
		}
		later := a.expr(span.node, vars, f)
		if len(later) != span.end-span.start {
			continue
		}
		for j, v := range later {
			i := span.start + j
			if semanticKey(args[i], vars.store) != semanticKey(v, vars.store) {
				a.diagnostic(n, "evaluation order may change argument value")
				args[i] = union(args[i], a.copyValue(v, vars.store))
			}
		}
	}
	var results []value
	// Alternative callees start from the same store; one alternative's captured
	// writes must not affect the next alternative's input.
	joined := map[string]value{}
	for _, target := range targets {
		branch := clone(vars)
		var rs []value
		switch {
		case target.fn != nil:
			fn := target.fn
			callArgs := args
			if fn.methodExpression {
				if len(args) == 0 {
					a.diagnostic(n, "method expression needs receiver")
					rs = []value{unknown()}
					break
				}
				copied := *fn
				copied.receiver = dereferenceObjects(args[0], branch.store)
				converted := append(value(nil), copied.receiver...)
				for i, r := range converted {
					if r.object == nil {
						continue
					}
					obj := *r.object
					obj.pointer = copied.receiverPointer
					if !obj.pointer {
						obj.binding = ""
					}
					converted[i].object = &obj
				}
				copied.receiver = converted
				for _, r := range args[0] {
					if r.object == nil || r.object.pointer != copied.expressionPointer {
						a.diagnostic(n, "method expression receiver shape mismatch")
					}
				}
				copied.methodExpression = false
				fn = &copied
				callArgs = args[1:]
			}
			rs = a.invoke(fn, callArgs, f.depth+1, n, branch.store)
		case target.kind == "summary":
			rs = a.summary(target.text, args, n)
		case target.kind == "opaque":
			a.diagnostic(n, "opaque call: "+target.text)
			rs = a.boundary(target.text, args, []value{unknown()})
		default:
			a.diagnostic(n, "dynamic call target: "+child(n, "Fun").Text)
			rs = []value{unknown()}
		}
		for j, v := range rs {
			for len(results) <= j {
				results = append(results, nil)
			}
			results[j] = union(results[j], v)
		}
		for id, v := range branch.store {
			joined[id] = union(joined[id], v)
		}
	}
	for id := range vars.store {
		delete(vars.store, id)
	}
	for id, v := range joined {
		vars.store[id] = v
	}
	if len(results) == 0 {
		return []value{unknown()}
	}
	return results
}
func (a *analyzer) summary(target string, args []value, n *inspect.Node) []value {
	if target == "strconv.Atoi" && len(args) == 1 {
		var converted value
		for _, v := range args[0] {
			if p := a.params[v.origin]; p != nil {
				p.Schema.Type = "integer"
				a.evidence(p, n.Pos)
				converted = union(converted, value{{kind: "integer", origin: v.origin}})
			} else {
				converted = union(converted, unknown())
			}
		}
		return a.boundary(target, args, []value{converted, unknown()})
	}
	var out value
	switch {
	case target == "url.Query" && len(args) == 0:
		out = scalar("query", "")
	case target == "query.Get" && len(args) == 1:
		out = a.parameter(n, "query", args[0])
	case target == "header.Get" && len(args) == 1:
		out = a.parameter(n, "header", args[0])
	case target == "request.PathValue" && len(args) == 1:
		out = a.parameter(n, "path", args[0])
	default:
		a.diagnostic(n, "summary argument mismatch: "+target)
		out = unknown()
	}
	return a.boundary(target, args, []value{out})
}
