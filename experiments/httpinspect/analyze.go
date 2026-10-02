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
	"github.com/podhmo/minigo/runtime"
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

type atom struct{ kind, text, origin string }
type value []atom
type env map[string]value
type flow struct {
	vars     env
	returned []value
	done     bool
}
type frame struct {
	decl    *inspect.Decl
	imports map[string]string
	depth   int
}
type analyzer struct {
	engine    *minigo.Engine
	ctx       context.Context
	limits    Limits
	report    Report
	params    map[string]*Parameter
	diagnosed map[string]bool
	exhausted bool
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
	a.invoke(decl, args, 0, nil)
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
func clone(v env) env {
	out := env{}
	for k, x := range v {
		out[k] = x
	}
	return out
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
		if x.text != "" {
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
func (a *analyzer) invoke(d *inspect.Decl, args []value, depth int, site *inspect.Node) []value {
	if depth >= a.limits.Depth {
		a.diagnostic(site, "call depth limit: "+d.Name)
		return []value{unknown()}
	}
	body, err := inspect.BodyOf(d)
	if err != nil || body == nil {
		a.diagnostic(site, "source body unavailable: "+d.Name)
		return []value{unknown()}
	}
	sig, err := inspect.SignatureOf(d)
	if err != nil {
		a.diagnostic(site, err.Error())
		return []value{unknown()}
	}
	f := frame{decl: d, depth: depth, imports: map[string]string{}}
	for _, sf := range d.Package.Files {
		if sf.Name == d.File {
			for _, im := range inspect.ImportsOf(inspect.NewFile(d.Package, sf)) {
				f.imports[im.Name] = im.Path
			}
		}
	}
	for _, field := range sig.ParamFields() {
		if field.Type.Kind == "Ellipsis" {
			a.diagnostic(site, "variadic function binding unsupported")
		}
	}
	if fields, _ := inspect.TypeParamsOf(d); len(fields) > 0 {
		a.diagnostic(site, "generic function binding unsupported")
	}
	vars := env{}
	i := 0
	for _, field := range sig.ParamFields() {
		for _, name := range field.Names {
			v := unknown()
			if i < len(args) {
				v = args[i]
			}
			vars[name] = v
			i++
		}
	}
	for _, field := range sig.ResultFields() {
		for _, name := range field.Names {
			vars[name] = unknown()
		}
	}
	if sig.Recv != nil {
		a.diagnostic(site, "method receiver binding is unsupported")
	}
	flows := a.block(body, []flow{{vars: vars}}, f)
	var result []value
	for _, p := range flows {
		rs := p.returned
		if !p.done && len(sig.ResultFields()) > 0 {
			a.diagnostic(body, "result fallthrough is unsupported")
			rs = []value{unknown()}
		}
		for j, v := range rs {
			for len(result) <= j {
				result = append(result, nil)
			}
			result[j] = union(result[j], v)
		}
	}
	a.report.Calls = append(a.report.Calls, Trace{Target: d.Package.Path + "." + d.Name, Args: describeAll(args), Results: describeAll(result)})
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
		var vals []value
		for _, r := range rhs {
			vals = append(vals, a.expr(r, p.vars, f)...)
		}
		// Evaluate all RHS expressions before writing any LHS slot.
		for i, l := range lhs {
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
			p.vars[l.Text] = v
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
				p.vars[name.Text] = v
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
			sig, _ := inspect.SignatureOf(f.decl)
			for _, field := range sig.ResultFields() {
				for _, name := range field.Names {
					p.returned = append(p.returned, p.vars[name])
				}
			}
		}
	case "IfStmt":
		local := clone(p.vars)
		initShadows := map[string]bool{}
		if init := child(n, "Init"); init != nil {
			a.stmt(init, flow{vars: local}, f)
			if init.Kind == "AssignStmt" && init.Token == ":=" {
				for _, id := range children(init, "Lhs") {
					initShadows[id.Text] = true
				}
			}
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
		// Keep alternatives separate. Branch locals do not escape the if scope.
		all := append(then, other...)
		for i := range all {
			for k := range all[i].vars {
				if old, ok := p.vars[k]; !ok {
					delete(all[i].vars, k)
				} else if initShadows[k] {
					all[i].vars[k] = old
				}
			}
		}
		return all
	case "BlockStmt":
		return a.scopedBlock(n, p, f)
	case "EmptyStmt":
	default:
		a.diagnostic(n, "statement unsupported: "+n.Kind)
		for name := range p.vars {
			p.vars[name] = unknown()
		}
		// Do not inspect an unsupported subtree with fabricated execution semantics.
	}
	return []flow{p}
}
func (a *analyzer) scopedBlock(n *inspect.Node, p flow, f frame) []flow {
	outer := p.vars
	p.vars = clone(outer)
	out := a.block(n, []flow{p}, f)
	// Restore names introduced by := in this scope, including shadows. Existing
	// variables assigned with = retain their new value.
	shadows := map[string]bool{}
	for _, s := range children(n, "List") {
		if s.Kind == "AssignStmt" && s.Token == ":=" {
			for _, l := range children(s, "Lhs") {
				if l.Kind == "Ident" {
					shadows[l.Text] = true
				}
			}
		}
		if s.Kind == "DeclStmt" {
			for _, sp := range children(child(s, "Decl"), "Specs") {
				for _, id := range children(sp, "Names") {
					shadows[id.Text] = true
				}
			}
		}
	}
	for i := range out {
		for k := range out[i].vars {
			if v, ok := outer[k]; !ok {
				delete(out[i].vars, k)
			} else if shadows[k] {
				out[i].vars[k] = v
			}
		}
	}
	return out
}
func (a *analyzer) expr(n *inspect.Node, vars env, f frame) []value {
	if n == nil || !a.tick(n) {
		return []value{unknown()}
	}
	switch n.Kind {
	case "Ident":
		if v, ok := vars[n.Text]; ok {
			return []value{v}
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
		x := a.expr(child(n, "X"), vars, f)[0]
		name := child(n, "Sel").Text
		var out value
		for _, v := range x {
			kind := "unknown"
			if v.kind == "request" && name == "URL" {
				kind = "url"
			}
			if v.kind == "request" && name == "Header" {
				kind = "header"
			}
			out = union(out, scalar(kind, ""))
		}
		return []value{out}
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
	fun := child(n, "Fun")
	var args []value
	for _, arg := range children(n, "Args") {
		vs := a.expr(arg, vars, f)
		args = append(args, vs...)
	}
	if fun == nil {
		a.diagnostic(n, "missing call target")
		return []value{unknown()}
	}
	if fun.Kind == "SelectorExpr" {
		recv, name := child(fun, "X"), child(fun, "Sel").Text
		if recv.Kind == "Ident" {
			if _, shadow := vars[recv.Text]; !shadow {
				if path, ok := f.imports[recv.Text]; ok {
					if path == "strconv" && name == "Atoi" && len(args) == 1 {
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
						return a.boundary("strconv.Atoi", args, []value{converted, unknown()})
					}
					pkg, err := a.engine.Package(a.ctx, path)
					if err != nil {
						a.diagnostic(n, err.Error())
						return []value{unknown()}
					}
					if pkg.Standard || pkg.Index == nil {
						a.diagnostic(n, "opaque call: "+path+"."+name)
						return a.boundary(path+"."+name, args, []value{unknown()})
					}
					return a.sourceCall(pkg, name, args, f, n)
				}
			}
		}
		receivers := a.expr(recv, vars, f)[0]
		var out value
		matched := false
		for _, r := range receivers {
			var v value
			switch {
			case r.kind == "url" && name == "Query" && len(args) == 0:
				v = scalar("query", "")
			case r.kind == "query" && name == "Get" && len(args) == 1:
				v = a.parameter(n, "query", args[0])
			case r.kind == "header" && name == "Get" && len(args) == 1:
				v = a.parameter(n, "header", args[0])
			case r.kind == "request" && name == "PathValue" && len(args) == 1:
				v = a.parameter(n, "path", args[0])
			default:
				a.diagnostic(n, "opaque method: "+r.kind+"."+name)
				v = unknown()
			}
			matched = true
			out = union(out, v)
		}
		if matched {
			return a.boundary("request-model."+name, args, []value{out})
		}
	}
	if fun.Kind == "Ident" {
		if _, shadow := vars[fun.Text]; !shadow {
			return a.sourceCall(f.decl.Package, fun.Text, args, f, n)
		}
	}
	a.diagnostic(n, "dynamic call target: "+fun.Text)
	return a.boundary(fun.Text, args, []value{unknown()})
}
func (a *analyzer) sourceCall(pkg *runtime.Package, name string, args []value, f frame, n *inspect.Node) []value {
	if pkg.Index != nil {
		if d := pkg.Index.Funcs[name]; d != nil {
			return a.invoke(inspect.NewDecl(pkg, d), args, f.depth+1, n)
		}
	}
	a.diagnostic(n, "unresolved function: "+name)
	return []value{unknown()}
}
