// Package main is the experiment's analyzer: it infers the request
// parameters of net/http handlers entirely from minigo.dev/inspect's
// new Ops view — the handler bodies lifted to a value-dataflow op list.
//
// The interesting part is that all of the analysis happens on the
// script side: Ops gives flat ops with explicit value ids (Op.Out /
// Op.Ins), resolved callee symbols (Op.Sym), declared types on coerces
// (Op.Typ) and nested bodies for func literals (Op.Body). Nothing here
// executes the app — bound stdlib calls stop at Kind:"host" decls.
package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/podhmo/minigo/inspect"
)

const appPkg = "github.com/podhmo/minigo/testdata/inspectapp"

// read is one inferred request parameter read.
type read struct {
	name string
	kind string
}

// eval is the per-body interpretation state: value-id keyed maps over
// the op list. req marks request-derived values, lit carries literal
// text, shape seeds caller-side chains for parameters, and ptype keeps
// declared param types for method-value resolution.
type eval struct {
	def    map[int]*inspect.Op
	req    map[int]bool
	lit    map[int]string
	shape  map[int]string
	ptype  map[int]*inspect.TypeExpr
	slotT  map[int]*inspect.TypeExpr
	reads  map[int]read
	retype map[int]string
	argRe  map[int]string
	retv   []int
	lines  []string
}

// anlz is the whole-package analyzer state shared across frames.
// mod is the module path prefix — only packages under it are descended
// into, keeping interpretation shallow (stdlib and deps stop here).
type anlz struct {
	mod    string
	pkgs   map[string]any
	memo   map[*inspect.Body]map[string]*eval
	active map[*inspect.Body]bool
	routes []*route
}

// route is one registered route and its resolved handler body.
type route struct {
	method string
	path   string
	name   string
	body   *inspect.Body
}

// Analyze infers the route table of the app package at path.
func Analyze(path string) string {
	a := &anlz{
		pkgs:   map[string]any{},
		memo:   map[*inspect.Body]map[string]*eval{},
		active: map[*inspect.Body]bool{},
	}
	p := inspect.PackageOf(path)
	a.mod = moduleOf(p)
	for _, d := range inspect.Decls(p) {
		if d.Kind != "func" && d.Kind != "method" {
			continue
		}
		b := inspect.Ops(d)
		if b == nil {
			continue
		}
		scanRoutes(a, b)
	}
	lines := []string{}
	for _, rt := range a.routes {
		seen := map[string]bool{}
		ls := []string{}
		if rt.body != nil {
			e := evalBody(a, rt.body, nil, nil, nil, 0)
			for _, l := range e.lines {
				seen[l] = true
			}
		}
		for _, nm := range pathParams(rt.path) {
			seen[nm+":path:string"] = true
		}
		for k := range seen {
			ls = append(ls, k)
		}
		sort.Strings(ls)
		head := rt.path
		if rt.method != "" {
			head = rt.method + " " + rt.path
		}
		lines = append(lines, head+": "+strings.Join(ls, "; "))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// scanRoutes walks an op list for HandleFunc/Handle registrations and
// resolves the handler argument into a body. Nested funclit bodies are
// scanned too.
func scanRoutes(a *anlz, b *inspect.Body) {
	e := newEval(b, nil, nil, nil)
	for _, op := range b.Ops {
		if op.Kind == "func" && op.Body != nil {
			scanRoutes(a, op.Body)
		}
		if op.Kind != "call" {
			continue
		}
		fch := chainOf(e, int(op.Fun))
		if !strings.HasSuffix(fch, "HandleFunc") && !strings.HasSuffix(fch, ".Handle") {
			continue
		}
		if len(op.Args) < 2 {
			continue
		}
		pat := litOf(e, int(op.Args[0]))
		if pat == "" {
			continue
		}
		registerRoute(a, e, int(op.Args[1]), pat)
	}
}

// registerRoute resolves the handler value of a registration call.
func registerRoute(a *anlz, e *eval, hv int, pat string) {
	rt := &route{method: "", path: pat}
	if i := strings.Index(pat, " "); i >= 0 {
		rt.method = pat[:i]
		rt.path = strings.TrimSpace(pat[i+1:])
	}
	d := e.def[hv]
	for d != nil && (d.Kind == "ref" || d.Kind == "coerce" || d.Kind == "deref" || d.Kind == "bind") {
		if len(d.Ins) == 0 {
			break
		}
		hv = int(d.Ins[0])
		d = e.def[hv]
	}
	if d == nil {
		a.routes = append(a.routes, rt)
		return
	}
	if d.Kind == "func" && d.Body != nil {
		rt.body = d.Body
		rt.name = d.Body.Name
	} else if d.Sym.Name != "" {
		rt.name = d.Sym.Name
		if dd := findDecl(a, d.Sym.PackagePath, d.Sym.Name); dd != nil {
			rt.body = inspect.Ops(dd)
		}
	} else if d.Kind == "sel" && len(d.Ins) > 0 {
		rt.name = d.Name
		if dd := methodDecl(a, e, d); dd != nil {
			rt.body = inspect.Ops(dd)
		}
	}
	a.routes = append(a.routes, rt)
}

// pathParams extracts {name} placeholders from a route pattern.
func pathParams(path string) []string {
	out := []string{}
	for {
		i := strings.Index(path, "{")
		if i < 0 {
			return out
		}
		j := strings.Index(path[i:], "}")
		if j < 0 {
			return out
		}
		out = append(out, path[i+1:i+j])
		path = path[i+j+1:]
	}
}

// evalBody interprets one body: propagates value classes over the op
// list, classifies request reads, and descends into user callees.
// argReq/argShape/argLit align with body.Params (receiver first).
func evalBody(a *anlz, b *inspect.Body, argReq []bool, argShape []string, argLit []string, depth int) *eval {
	if b == nil {
		return nil
	}
	key := sigOf(argReq, argShape, argLit)
	if m, ok := a.memo[b]; ok {
		if r, ok2 := m[key]; ok2 {
			return r
		}
	} else {
		a.memo[b] = map[string]*eval{}
	}
	if a.active[b] {
		return nil
	}
	a.active[b] = true
	e := newEval(b, argReq, argShape, argLit)
	for _, op := range b.Ops {
		classifyOp(a, e, op, depth)
	}
	for v, rd := range e.reads {
		t := "string"
		if rt, ok := e.retype[v]; ok {
			t = rt
		}
		e.lines = append(e.lines, rd.name+":"+rd.kind+":"+t)
	}
	for i, p := range b.Params {
		if rt, ok := e.retype[int(p.Val)]; ok {
			e.argRe[i] = rt
		}
	}
	delete(a.active, b)
	a.memo[b][key] = e
	return e
}

// newEval builds the frame: def map plus parameter seeding.
func newEval(b *inspect.Body, argReq []bool, argShape []string, argLit []string) *eval {
	e := &eval{
		def:    map[int]*inspect.Op{},
		req:    map[int]bool{},
		lit:    map[int]string{},
		shape:  map[int]string{},
		ptype:  map[int]*inspect.TypeExpr{},
		slotT:  map[int]*inspect.TypeExpr{},
		reads:  map[int]read{},
		retype: map[int]string{},
		argRe:  map[int]string{},
	}
	for _, op := range b.Ops {
		for k := 0; k < op.NOut; k++ {
			e.def[op.Out+k] = op
		}
	}
	for i, p := range b.Params {
		v := int(p.Val)
		e.ptype[v] = p.Type
		e.slotT[p.Slot] = p.Type
		if i < len(argReq) && argReq[i] {
			e.req[v] = true
		}
		if i < len(argShape) && argShape[i] != "" {
			e.shape[v] = argShape[i]
		}
		if i < len(argLit) && argLit[i] != "" {
			e.lit[v] = argLit[i]
		}
		if isReqType(p.Type) {
			e.req[v] = true
		}
		if isValsType(p.Type) && e.shape[v] == "" {
			e.shape[v] = ".Query()"
		}
	}
	return e
}

func sigOf(rq []bool, sh []string, li []string) string {
	s := ""
	for i, r := range rq {
		if r {
			s += fmt.Sprintf("%d:req;", i)
		}
	}
	for i, x := range sh {
		if x != "" {
			s += fmt.Sprintf("%d:%s;", i, x)
		}
	}
	for i, x := range li {
		if x != "" {
			s += fmt.Sprintf("%d:%s;", i, x)
		}
	}
	return s
}

// classifyOp propagates value classes through one op and dispatches
// read detection / call descent.
func classifyOp(a *anlz, e *eval, op *inspect.Op, depth int) {
	if op.Out >= 0 {
		for k := 0; k < op.NOut; k++ {
			v := op.Out + k
			for _, iv := range op.Ins {
				if e.req[int(iv)] {
					e.req[v] = true
				}
			}
			if op.Kind == "lit" && k == 0 {
				e.lit[v] = op.Lit
			} else if len(op.Ins) == 1 && passthrough(op.Kind) {
				e.lit[v] = e.lit[int(op.Ins[0])]
			}
		}
	}
	if op.Kind == "coerce" && op.Slot >= 0 && op.Typ != nil {
		e.slotT[op.Slot] = op.Typ
	}
	if op.Kind == "ret" {
		for _, iv := range op.Ins {
			e.retv = append(e.retv, int(iv))
		}
		return
	}
	if (op.Kind == "index" || op.Kind == "inst") && len(op.Ins) == 2 {
		classifyIndex(e, op)
		return
	}
	if op.Kind == "call" || op.Kind == "defer" || op.Kind == "go" {
		handleCall(a, e, op, depth)
	}
}

// passthrough kinds copy the literal of their single input.
func passthrough(k string) bool {
	return k == "ref" || k == "coerce" || k == "deref" || k == "assert" || k == "move"
}

// classifyIndex handles base[key] reads like r.URL.Query()["tag"] —
// OpIndex and OpInstantiate both encode indexing.
func classifyIndex(e *eval, op *inspect.Op) {
	base := int(op.Ins[0])
	if !e.req[base] {
		return
	}
	bch := chainOf(e, base)
	kind := ""
	if strings.HasSuffix(bch, "Query()") {
		kind = "query"
	}
	if strings.HasSuffix(bch, "PostForm()") {
		kind = "form"
	}
	if kind == "" {
		return
	}
	name := litOf(e, int(op.Ins[1]))
	if name != "" {
		e.reads[op.Out] = read{name: name, kind: kind}
	}
}

// handleCall classifies a call op: conversion retypes, request reads,
// body decodes, and descends into resolvable user callees.
func handleCall(a *anlz, e *eval, op *inspect.Op, depth int) {
	fch := chainOf(e, int(op.Fun))
	base := selInput(e, int(op.Fun))
	s := op.Sym

	if s.PackagePath == "strconv" && len(op.Args) > 0 {
		t := ""
		switch s.Name {
		case "Atoi", "ParseInt", "ParseUint":
			t = "int"
		case "ParseBool":
			t = "bool"
		case "ParseFloat":
			t = "float"
		}
		if t != "" {
			for _, av := range op.Args {
				if e.req[int(av)] {
					e.retype[int(av)] = t
				}
			}
		}
		return
	}

	kind := ""
	switch {
	case strings.HasSuffix(fch, ".PathValue"):
		kind = "path"
	case strings.HasSuffix(fch, ".FormValue") || strings.HasSuffix(fch, ".PostFormValue"):
		kind = "form"
	case strings.HasSuffix(fch, ".Cookie"):
		kind = "cookie"
	case strings.HasSuffix(fch, ".Header.Get") || strings.HasSuffix(fch, ".Header.Values"):
		kind = "header"
	case strings.HasSuffix(fch, ".Query().Get") || strings.HasSuffix(fch, ".Query().Has"):
		kind = "query"
	case strings.HasSuffix(fch, ".PostForm().Get"):
		kind = "form"
	case strings.HasSuffix(fch, ".Get") && e.req[base]:
		kind = "query"
	}
	if kind != "" && e.req[base] && len(op.Args) > 0 {
		name := litOf(e, int(op.Args[0]))
		if name != "" {
			e.reads[op.Out] = read{name: name, kind: kind}
		}
	}

	if strings.HasSuffix(fch, ".Decode") {
		classifyDecode(e, op)
	}

	if depth < 8 {
		descend(a, e, op, depth)
	}
}

// classifyDecode spots json.NewDecoder(r.Body).Decode(&v).
func classifyDecode(e *eval, op *inspect.Op) {
	fdef := e.def[int(op.Fun)]
	if fdef == nil || len(fdef.Ins) == 0 {
		return
	}
	cres := int(fdef.Ins[0])
	cdef := e.def[cres]
	if cdef == nil || cdef.Kind != "call" || cdef.Sym.Name != "NewDecoder" {
		return
	}
	reqOK := false
	for _, av := range cdef.Args {
		if e.req[int(av)] {
			reqOK = true
		}
	}
	if !reqOK {
		return
	}
	name := "body"
	if len(op.Args) > 0 {
		if t := typeOfVal(e, int(op.Args[0])); t != "" {
			name = t
		}
	}
	e.reads[op.Out] = read{name: name, kind: "body"}
}

// typeOfVal chases a value's declared type via its def slot and the
// frame's coerce-table — used for &v decode targets.
func typeOfVal(e *eval, v int) string {
	d := e.def[v]
	if d == nil {
		return ""
	}
	if d.Slot >= 0 {
		if t := e.slotT[d.Slot]; t != nil {
			u := inspect.UnRef(t)
			if sid := inspect.SymbolID(u); sid != nil {
				return sid.Name
			}
		}
	}
	if len(d.Ins) > 0 && passthrough(d.Kind) {
		return typeOfVal(e, int(d.Ins[0]))
	}
	return ""
}

// descend interprets a resolvable callee body with the call site's
// value classes, then merges sub-results back into the caller frame.
func descend(a *anlz, e *eval, op *inspect.Op, depth int) {
	var cb *inspect.Body
	recv := -1
	fdef := e.def[int(op.Fun)]
	if fdef != nil && fdef.Kind == "func" && fdef.Body != nil {
		cb = fdef.Body
	} else if s := op.Sym; s.Name != "" && !strings.HasPrefix(s.PackagePath, ":") {
		if d := findDecl(a, s.PackagePath, s.Name); d != nil {
			cb = inspect.Ops(d)
		}
	} else if fdef != nil && fdef.Kind == "sel" && len(fdef.Ins) > 0 {
		if d := methodDecl(a, e, fdef); d != nil {
			cb = inspect.Ops(d)
			recv = int(fdef.Ins[0])
		}
	}
	if cb == nil || a.active[cb] {
		return
	}
	argReq := []bool{}
	argShape := []string{}
	argLit := []string{}
	if recv >= 0 {
		argReq = append(argReq, e.req[recv])
		argShape = append(argShape, chainOf(e, recv))
		argLit = append(argLit, litOf(e, recv))
	}
	argv := []int{}
	for _, av := range op.Args {
		argv = append(argv, int(av))
	}
	for _, v := range argv {
		argReq = append(argReq, e.req[v])
		argShape = append(argShape, chainOf(e, v))
		argLit = append(argLit, litOf(e, v))
	}
	sub := evalBody(a, cb, argReq, argShape, argLit, depth+1)
	if sub == nil {
		return
	}
	for _, rv := range sub.retv {
		if sub.req[rv] && op.Out >= 0 {
			e.req[op.Out] = true
		}
	}
	if op.Out >= 0 && len(sub.retv) > 0 {
		l0 := litOf(sub, sub.retv[0])
		ok := l0 != ""
		for _, rv := range sub.retv {
			if litOf(sub, rv) != l0 {
				ok = false
			}
		}
		if ok {
			e.lit[op.Out] = l0
		}
	}
	for i, rt := range sub.argRe {
		av := -1
		if recv >= 0 {
			if i == 0 {
				av = recv
			} else if i-1 < len(argv) {
				av = argv[i-1]
			}
		} else if i < len(argv) {
			av = argv[i]
		}
		if av >= 0 {
			e.retype[av] = rt
		}
	}
	e.lines = append(e.lines, sub.lines...)
}

// chainOf renders a value's expression chain for suffix matching.
func chainOf(e *eval, v int) string {
	d := e.def[v]
	if d == nil {
		if s, ok := e.shape[v]; ok {
			return s
		}
		return "?"
	}
	switch d.Kind {
	case "lit":
		return "lit:" + d.Lit
	case "global":
		return d.Name
	case "sel":
		if len(d.Ins) > 0 {
			return chainOf(e, int(d.Ins[0])) + "." + d.Name
		}
	case "call", "defer", "go", "special":
		return chainOf(e, int(d.Fun)) + "()"
	case "func":
		return "func()"
	case "index", "inst":
		if len(d.Ins) > 0 {
			return chainOf(e, int(d.Ins[0])) + "[]"
		}
	case "ref", "coerce", "deref", "assert", "move":
		if len(d.Ins) > 0 {
			if s, ok := e.shape[int(d.Ins[0])]; ok && s != "" {
				return s
			}
			if d.Kind == "ref" && d.Text != "" {
				return d.Text
			}
			return chainOf(e, int(d.Ins[0]))
		}
	}
	return d.Kind
}

// selInput returns the receiver value a select applies to — the value
// the method is called on (`r.URL.Query().Get` → the Query() result,
// `r.PathValue` → r itself). Request-read checks test its req class,
// not the deepest chain base, so helpers returning their request arg
// (ReqOf(r).URL.Query()...) stay request-derived.
func selInput(e *eval, fun int) int {
	if d := e.def[fun]; d != nil && d.Kind == "sel" && len(d.Ins) > 0 {
		return int(d.Ins[0])
	}
	return fun
}

// litOf resolves the literal text a value carries, if any.
func litOf(e *eval, v int) string {
	if s, ok := e.lit[v]; ok {
		return s
	}
	d := e.def[v]
	if d == nil {
		return ""
	}
	if d.Kind == "lit" {
		return d.Lit
	}
	if len(d.Ins) == 1 && passthrough(d.Kind) {
		return litOf(e, int(d.Ins[0]))
	}
	return ""
}

// moduleOf derives the module path prefix of a package by matching the
// suffix of its import path against the suffix of its directory.
func moduleOf(p any) string {
	path := inspect.Path(p)
	dir := inspect.Dir(p)
	rest := path
	for {
		i := strings.Index(rest, "/")
		if i < 0 {
			return path
		}
		rest = rest[i+1:]
		if strings.HasSuffix(dir, rest) {
			return path[:len(path)-len(rest)-1]
		}
	}
}

// sourcePkg reports whether a package is user source worth descending
// into: only packages inside the analyzed module qualify, so PackageOf
// (which traps on unresolvable paths) is never asked for stdlib or
// third-party code — the shallow-interpretation boundary.
func sourcePkg(a *anlz, path string) any {
	if path != a.mod && !strings.HasPrefix(path, a.mod+"/") {
		return nil
	}
	if v, ok := a.pkgs[path]; ok {
		return v
	}
	p := inspect.PackageOf(path)
	a.pkgs[path] = p
	return p
}

// findDecl looks up a name in a package without trapping: bound
// packages expose no Decls, so stdlib stops here naturally.
func findDecl(a *anlz, path string, name string) *inspect.Decl {
	p := sourcePkg(a, path)
	if p == nil {
		return nil
	}
	var found *inspect.Decl
	for _, d := range inspect.Decls(p) {
		if d.Name == name {
			found = d
		}
		if d.Kind == "type" {
			for _, m := range inspect.Methods(d) {
				if m.Name == name {
					found = m
				}
			}
		}
	}
	return found
}

// methodDecl resolves a method-value select like srv.DeleteItem via
// the receiver base's declared type (param type or slot coerce type).
func methodDecl(a *anlz, e *eval, seldef *inspect.Op) *inspect.Decl {
	basev := int(seldef.Ins[0])
	typ := e.ptype[basev]
	if typ == nil {
		if d := e.def[basev]; d != nil && d.Slot >= 0 {
			typ = e.slotT[d.Slot]
		}
	}
	if typ == nil {
		return nil
	}
	u := inspect.UnRef(typ)
	sid := inspect.SymbolID(u)
	if sid == nil || sid.Name == "" {
		return nil
	}
	p := sourcePkg(a, sid.PackagePath)
	if p == nil {
		return nil
	}
	for _, d := range inspect.Decls(p) {
		if d.Kind != "type" || d.Name != sid.Name {
			continue
		}
		for _, m := range inspect.Methods(d) {
			if m.Name == seldef.Name {
				return m
			}
		}
	}
	return nil
}

// isReqType reports whether a param type is *net/http.Request.
func isReqType(t *inspect.TypeExpr) bool {
	return typeIs(t, "net/http", "Request")
}

// isValsType reports whether a param type is net/url.Values.
func isValsType(t *inspect.TypeExpr) bool {
	return typeIs(t, "net/url", "Values")
}

func typeIs(t *inspect.TypeExpr, path string, name string) bool {
	if t == nil {
		return false
	}
	u := inspect.UnRef(t)
	if u == nil {
		return false
	}
	sid := inspect.SymbolID(u)
	return sid != nil && sid.PackagePath == path && sid.Name == name
}
