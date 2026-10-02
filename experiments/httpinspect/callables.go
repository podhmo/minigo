package httpinspect

import (
	"github.com/podhmo/minigo/inspect"
	"github.com/podhmo/minigo/runtime"
)

type parameter struct {
	name string
	typ  *inspect.TypeExpr
}
type callable struct {
	name              string
	owner             *inspect.Decl
	body              *inspect.Node
	params, results   []parameter
	captures          map[string]string
	receiver          value
	receiverName      string
	methodExpression  bool
	receiverPointer   bool
	expressionPointer bool
	variadic, generic bool
}

// Object identity is a set of field slots, separate from the value/pointer
// shape. Path stores carry the values; the object and callable handles are
// immutable, so cloning a path does not copy stale captured environments.
type sourceObject struct {
	typ     *inspect.Decl
	fields  map[string]string
	pointer bool
	binding string // address of a variable whose whole struct may be reassigned
}

func fieldsToParams(fs []*inspect.Field) []parameter {
	var out []parameter
	for _, field := range fs {
		if len(field.Names) == 0 {
			out = append(out, parameter{typ: field.Type})
		}
		for _, name := range field.Names {
			out = append(out, parameter{name: name, typ: field.Type})
		}
	}
	return out
}
func sourceFunction(d *inspect.Decl) *callable {
	fn := &callable{name: d.Package.Path + "." + d.Name, owner: d}
	fn.body, _ = inspect.BodyOf(d)
	if sig, err := inspect.SignatureOf(d); err == nil {
		fn.params = fieldsToParams(sig.ParamFields())
		fn.results = fieldsToParams(sig.ResultFields())
		if sig.Recv != nil {
			fn.receiverPointer = sig.Recv.Type.Kind == "StarExpr"
		}
		if sig.Recv != nil && len(sig.Recv.Names) > 0 {
			fn.receiverName = sig.Recv.Names[0]
		}
	}
	for _, p := range fn.params {
		if p.typ != nil && p.typ.Kind == "Ellipsis" {
			fn.variadic = true
		}
	}
	if fields, _ := inspect.TypeParamsOf(d); len(fields) > 0 {
		fn.generic = true
	}
	return fn
}
func importsOf(d *inspect.Decl) map[string]string {
	out := map[string]string{}
	for _, sf := range d.Package.Files {
		if sf.Name == d.File {
			for _, im := range inspect.ImportsOf(inspect.NewFile(d.Package, sf)) {
				out[im.Name] = im.Path
			}
		}
	}
	return out
}
func nodeParams(n *inspect.Node) []parameter {
	var out []parameter
	for _, field := range children(n, "List") {
		typ := child(field, "Type")
		var te *inspect.TypeExpr
		if typ != nil {
			te = typ.Type
		}
		names := children(field, "Names")
		if len(names) == 0 {
			out = append(out, parameter{typ: te})
		}
		for _, name := range names {
			out = append(out, parameter{name: name.Text, typ: te})
		}
	}
	return out
}
func (a *analyzer) literal(n *inspect.Node, vars env) value {
	typ := child(n, "Type")
	fn := &callable{name: n.Owner.Package.Path + ".func@" + n.Pos, owner: n.Owner, body: child(n, "Body"), params: nodeParams(child(typ, "Params")), results: nodeParams(child(typ, "Results")), captures: copyNames(vars.names)}
	for _, p := range fn.params {
		if p.typ != nil && p.typ.Kind == "Ellipsis" {
			fn.variadic = true
		}
	}
	return value{{kind: "closure", fn: fn}}
}
func (a *analyzer) copyValue(v value, store map[string]value) value {
	out := append(value(nil), v...)
	for i, x := range out {
		if x.object == nil || x.object.pointer || x.kind == "type" {
			continue
		}
		obj := &sourceObject{typ: x.object.typ, fields: map[string]string{}}
		for name, id := range x.object.fields {
			fresh := a.allocate()
			obj.fields[name] = fresh
			store[fresh] = a.copyValue(store[id], store)
		}
		out[i].object = obj
	}
	return out
}
func (a *analyzer) resolveType(n *inspect.Node) *inspect.Decl {
	if n == nil || n.Type == nil {
		a.diagnostic(n, "explicit source type required")
		return nil
	}
	sid, ok := n.Type.Unref().SymbolID()
	if !ok {
		a.diagnostic(n, "source type unresolved: "+n.Text)
		return nil
	}
	return a.typeDecl(sid, n)
}
func (a *analyzer) typeDecl(sid runtime.SymbolID, n *inspect.Node) *inspect.Decl {
	pkg, err := a.engine.Package(a.ctx, sid.PackagePath)
	if err != nil {
		a.diagnostic(n, err.Error())
		return nil
	}
	if pkg.Index == nil || pkg.Standard {
		a.diagnostic(n, "opaque type: "+sid.PackagePath+"."+sid.Name)
		return nil
	}
	if td := pkg.Index.Types[sid.Name]; td != nil {
		return inspect.NewDecl(pkg, td.Decl)
	}
	a.diagnostic(n, "source type unresolved: "+sid.Name)
	return nil
}
func (a *analyzer) composite(n *inspect.Node, vars env, f frame) value {
	typ := a.resolveType(child(n, "Type"))
	if typ == nil {
		return unknown()
	}
	fields, err := inspect.FieldsOf(typ)
	if err != nil {
		a.diagnostic(n, "only explicit source struct literals supported")
		return unknown()
	}
	obj := &sourceObject{typ: typ, fields: map[string]string{}}
	for _, field := range fields {
		if field.Embedded {
			a.diagnostic(n, "embedded fields unsupported")
			continue
		}
		for _, name := range field.Names {
			id := a.allocate()
			obj.fields[name] = id
			v := unknown()
			if field.Type.Text == "string" {
				v = scalar("literal", "")
			}
			vars.store[id] = v
		}
	}
	for _, elt := range children(n, "Elts") {
		if elt.Kind != "KeyValueExpr" {
			a.diagnostic(elt, "positional struct literal unsupported")
			continue
		}
		key := child(elt, "Key")
		rhs := a.expr(child(elt, "Value"), vars, f)[0]
		if id, ok := obj.fields[key.Text]; ok {
			vars.store[id] = a.copyValue(rhs, vars.store)
		} else {
			a.diagnostic(key, "unknown struct field: "+key.Text)
		}
	}
	return value{{kind: "object", text: typ.Package.Path + "." + typ.Name, object: obj}}
}
func (a *analyzer) address(n *inspect.Node, vars env, f frame) value {
	operand := child(n, "X")
	xs := a.expr(operand, vars, f)[0]
	var out value
	for _, x := range xs {
		if x.object == nil {
			a.diagnostic(n, "address operand unsupported")
			out = union(out, unknown())
			continue
		}
		if x.object.pointer {
			a.diagnostic(n, "pointer-to-pointer address unsupported")
			out = union(out, unknown())
			continue
		}
		obj := *x.object
		obj.pointer = true
		obj.binding = addressBinding(operand, vars)
		if obj.binding == "" && operand.Kind != "CompositeLit" {
			a.diagnostic(n, "address of nonlocal storage unsupported")
			out = union(out, unknown())
			continue
		}
		x.object = &obj
		out = union(out, value{x})
	}
	return out
}
func (a *analyzer) packageMember(path, name string, n *inspect.Node) value {
	if path == "strconv" && name == "Atoi" {
		return scalar("summary", "strconv.Atoi")
	}
	pkg, err := a.engine.Package(a.ctx, path)
	if err != nil {
		a.diagnostic(n, err.Error())
		return unknown()
	}
	if pkg.Standard || pkg.Index == nil {
		return scalar("opaque", path+"."+name)
	}
	if d := pkg.Index.Funcs[name]; d != nil {
		return value{{kind: "function", fn: sourceFunction(inspect.NewDecl(pkg, d))}}
	}
	if td := pkg.Index.Types[name]; td != nil {
		d := inspect.NewDecl(pkg, td.Decl)
		return value{{kind: "type", text: path + "." + name, object: &sourceObject{typ: d}}}
	}
	a.diagnostic(n, "unresolved package member: "+path+"."+name)
	return unknown()
}
func (a *analyzer) selectValue(n *inspect.Node, vars env, f frame) value {
	recv, name := child(n, "X"), child(n, "Sel").Text
	if recv.Kind == "Ident" {
		if _, shadow := vars.lookup(recv.Text); !shadow {
			if path, ok := f.imports[recv.Text]; ok {
				return a.packageMember(path, name, n)
			}
		}
	}
	xs := dereferenceObjects(a.expr(recv, vars, f)[0], vars.store)
	var out value
	for _, x := range xs {
		switch {
		case x.kind == "request" && name == "URL":
			out = union(out, scalar("url", ""))
		case x.kind == "request" && name == "Header":
			out = union(out, scalar("header", ""))
		case (x.kind == "url" && name == "Query") || (x.kind == "query" && name == "Get") || (x.kind == "header" && name == "Get") || (x.kind == "request" && name == "PathValue"):
			out = union(out, scalar("summary", x.kind+"."+name))
		case x.object != nil:
			if x.kind != "type" {
				if id, ok := x.object.fields[name]; ok {
					out = union(out, vars.store[id])
					continue
				}
			}
			out = union(out, a.method(x, name, n, vars))
		default:
			a.diagnostic(n, "opaque method or field: "+x.kind+"."+name)
			out = union(out, unknown())
		}
	}
	return out
}
func (a *analyzer) method(x atom, name string, n *inspect.Node, vars env) value {
	typ := x.object.typ
	methods, err := inspect.MethodsOf(typ)
	if err != nil {
		a.diagnostic(n, err.Error())
		return unknown()
	}
	for _, decl := range methods {
		if decl.Name == name {
			fn := sourceFunction(decl)
			fn.name = typ.Package.Path + "." + typ.Name + "." + name
			sig, _ := inspect.SignatureOf(decl)
			pointer := sig.Recv.Type.Kind == "StarExpr"
			if x.kind == "type" {
				fn.methodExpression = true
				fn.expressionPointer = x.object.pointer
				return value{{kind: "method", fn: fn}}
			}
			receiver := x
			obj := *x.object
			obj.pointer = pointer
			if !pointer {
				obj.binding = ""
			} else if !x.object.pointer {
				obj.binding = addressBinding(child(n, "X"), vars)
				if obj.binding == "" {
					a.diagnostic(n, "automatic address of nonlocal receiver unsupported")
					return unknown()
				}
			}
			receiver.object = &obj
			// Binding a value method snapshots its receiver, including when selected
			// through a pointer. Binding a pointer method retains the receiver's slots.
			fn.receiver = a.copyValue(value{receiver}, vars.store)
			return value{{kind: "method", fn: fn}}
		}
	}
	a.diagnostic(n, "method target unresolved: "+typ.Name+"."+name)
	return unknown()
}
func (a *analyzer) fieldSlots(n *inspect.Node, vars env, f frame) []string {
	recv := dereferenceObjects(a.expr(child(n, "X"), vars, f)[0], vars.store)
	name := child(n, "Sel").Text
	var slots []string
	for _, x := range recv {
		if x.object == nil {
			a.diagnostic(n, "field assignment receiver unsupported")
			continue
		}
		id, ok := x.object.fields[name]
		if !ok {
			a.diagnostic(n, "field assignment unresolved: "+name)
			continue
		}
		found := false
		for _, old := range slots {
			if old == id {
				found = true
			}
		}
		if !found {
			slots = append(slots, id)
		}
	}
	return slots
}
func addressBinding(n *inspect.Node, vars env) string {
	for n != nil && n.Kind == "ParenExpr" {
		n = child(n, "X")
	}
	if n != nil && n.Kind == "Ident" {
		return vars.names[n.Text]
	}
	return ""
}

func objectDescription(x atom) string {
	prefix := ""
	if x.object != nil && x.object.pointer {
		prefix = "*"
	}
	return prefix + x.text
}
func functionDescription(x atom) string {
	if x.fn != nil {
		return x.kind + ":" + x.fn.name
	}
	return x.kind
}

// Follow a pointer to a local's storage, rather than the struct value that was
// in that local when a method value or address expression was created.
func dereferenceObjects(xs value, store map[string]value) value {
	var out value
	for _, x := range xs {
		if x.object == nil || x.object.binding == "" {
			out = union(out, value{x})
			continue
		}
		current := store[x.object.binding]
		if len(current) == 0 {
			out = union(out, unknown())
			continue
		}
		for _, v := range current {
			if v.object == nil {
				out = union(out, unknown())
				continue
			}
			obj := *v.object
			obj.pointer = true
			obj.binding = x.object.binding
			v.object = &obj
			out = union(out, value{v})
		}
	}
	return out
}
