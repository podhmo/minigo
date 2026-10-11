// Package script is the body of the extract-schema tool, executed by
// the minigo interpreter (see ../main.go). It walks a package's type
// declarations through the inspect API and renders a JSON Schema
// document: struct graphs become $defs entries joined by $ref, fields
// become properties, and the walk stops — by policy — at boundaries
// where more analysis buys nothing (stdlib, bound host types) or
// where a plugin registered its own reading.
package script

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/podhmo/minigo/examples/extract-schema/extschema"
	"github.com/podhmo/minigo/inspect"
)

// Main extracts a JSON Schema for target (a package directory, or an
// import path with pkgMode). With typeName it emits the single-type
// form; with "" every type decl in the package lands in $defs,
// OpenAPI components.schemas style. overrides maps "pkgpath.Name" to
// a schema fragment consulted before the default named-type walk —
// the extension point plugin scripts feed.
func Main(target string, pkgMode bool, typeName string, stdlibMode string, loose bool, pluginDirs []string, title string, description string) (string, error) {
	pkg := inspect.DirOf(target)
	if pkgMode {
		pkg = inspect.PackageOf(target)
	}
	if pkg == nil {
		return "", fmt.Errorf("extract-schema: could not load package %q", target)
	}

	g := &generator{
		loose:     loose,
		stdlib:    stdlibMode,
		cfg:       &extschema.Config{},
		defs:      map[string]any{},
		seen:      map[string]string{},
		used:      map[string]int{},
		nameOwner: map[string]string{},
	}
	if err := g.loadPlugins(pluginDirs); err != nil {
		return "", err
	}

	final := map[string]any{}
	if typeName != "" {
		d := inspect.Symbol(pkg, typeName)
		if d == nil || inspect.Kind(d) != "type" {
			return "", fmt.Errorf("extract-schema: type %q not found in %s", typeName, inspect.Path(pkg))
		}
		name, err := g.defOf(d)
		if err != nil {
			return "", err
		}
		// The reference tool inlines a root used only once — unless its
		// def is itself a bare $ref, which stays a $ref for clarity.
		def, _ := g.defs[name].(map[string]any)
		if g.used[name] <= 1 && !isRefDoc(def) {
			for k, v := range def {
				final[k] = v
			}
			delete(g.defs, name)
		} else {
			final["$ref"] = "#/$defs/" + name
		}
	} else {
		for _, d := range inspect.Decls(pkg) {
			if inspect.Kind(d) != "type" {
				continue
			}
			if _, err := g.defOf(d); err != nil {
				return "", err
			}
		}
	}
	if len(g.defs) > 0 {
		final["$defs"] = g.defs
	}

	root := map[string]any{}
	root["$schema"] = "http://json-schema.org/draft-07/schema#"
	if title != "" {
		root["title"] = title
	}
	if description != "" {
		root["description"] = description
	}
	for k, v := range final {
		if k == "description" && description != "" {
			continue
		}
		root[k] = v
	}
	return emit(root, "\t"), nil
}

func isRefDoc(doc map[string]any) bool {
	if doc == nil {
		return false
	}
	for k := range doc {
		if k != "$ref" && k != "description" {
			return false
		}
	}
	_, ok := doc["$ref"]
	return ok
}

// warn reports a softened failure on stderr: the schema records the
// hole as a leaf, the run does not fake coverage it did not see.
func warn(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "extract-schema: "+format+"\n", args...)
}

type generator struct {
	loose  bool
	stdlib string
	// cfg is the shared registrar plugins' Configure(c) fills — a
	// named API surface (extschema) instead of untyped map keys.
	cfg       *extschema.Config
	defs      map[string]any
	seen      map[string]string // "pkgpath.Name" -> def name
	used      map[string]int
	nameOwner map[string]string // def name -> symbol key (collision check)
}

// loadPlugins loops the plugin dirs and calls each package's
// Configure(c *extschema.Config) — the single plugin entry point.
// Plugins configure the shared registrar in this same VM via
// inspect.Value; a dir without Configure is a warning, not a failure.
func (g *generator) loadPlugins(dirs []string) error {
	for _, dir := range dirs {
		pp := inspect.DirOf(dir)
		if pp == nil {
			return fmt.Errorf("extract-schema: could not load plugin %q", dir)
		}
		// inspect.Symbol traps on a miss, so scan Decls instead.
		if findFunc(inspect.Decls(pp), "Configure") == nil {
			warn("plugin %s exposes no Configure()", dir)
			continue
		}
		inspect.Value(pp, "Configure").(func(*extschema.Config))(g.cfg)
	}
	return nil
}

func findFunc(ds []*inspect.Decl, name string) *inspect.Decl {
	for _, d := range ds {
		if d.Name == name && inspect.Kind(d) == "func" {
			return d
		}
	}
	return nil
}

// defOf registers d as a $defs entry and returns its def name.
// Marking `seen` before walking the decl is what makes recursive
// types (`type Node struct { Next *Node }`) terminate.
func (g *generator) defOf(d *inspect.Decl) (string, error) {
	key := inspect.Path(d.Package) + "." + d.Name
	if name, ok := g.seen[key]; ok {
		g.used[name]++
		return name, nil
	}
	name := g.defName(key, d)
	g.seen[key] = name
	g.used[name] = 1
	doc, err := g.docForDecl(d)
	if err != nil {
		return "", err
	}
	g.defs[name] = doc
	return name, nil
}

func (g *generator) defName(key string, d *inspect.Decl) string {
	name := d.Name
	if owner, ok := g.nameOwner[name]; ok && owner != key {
		base := d.Name + "_" + inspect.Name(d.Package)
		name = base
		for i := 2; ; i++ {
			if owner, ok := g.nameOwner[name]; !ok || owner == key {
				break
			}
			name = fmt.Sprintf("%s%d", base, i)
		}
	}
	g.nameOwner[name] = key
	return name
}

// docForDecl renders the $defs body for one type declaration:
// enum members, alias indirection, then the shape of its Def.
func (g *generator) docForDecl(d *inspect.Decl) (map[string]any, error) {
	doc := map[string]any{}
	if d.Doc != "" {
		doc["description"] = g.declDoc(d, d.Doc)
	}
	def := inspect.Def(d)
	if inspect.IsAlias(d) && def != nil {
		sub, err := g.schemaFor(def)
		if err != nil {
			return nil, fmt.Errorf("alias %s: %w", d.Name, err)
		}
		for k, v := range sub {
			doc[k] = v
		}
	} else if def != nil {
		sub, err := g.schemaFor(def)
		if err != nil {
			return nil, fmt.Errorf("type %s: %w", d.Name, err)
		}
		for k, v := range sub {
			doc[k] = v
		}
	}
	if members := inspect.EnumMembers(d); len(members) > 0 {
		var vals []any
		for _, m := range members {
			v := inspect.Value(m.Package, m.Name)
			if v == nil {
				warn("enum member %s.%s has no value, skipped", d.Name, m.Name)
				continue
			}
			vals = append(vals, v)
		}
		if len(vals) > 0 {
			doc["enum"] = vals
		}
	}
	return doc, nil
}

// schemaFor renders a schema fragment for a type expression — the
// recursion heart: composites dispatch on TypeExpr.Kind, named types
// go through refFor.
func (g *generator) schemaFor(te *inspect.TypeExpr) (map[string]any, error) {
	switch te.Kind {
	case "StarExpr":
		return g.schemaFor(inspect.UnRef(te))
	case "ParenExpr":
		return g.schemaFor(inspect.Children(te)[0])
	case "ArrayType":
		items, err := g.schemaFor(inspect.Children(te)[0])
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items}, nil
	case "MapType":
		vals, err := g.schemaFor(inspect.Children(te)[1])
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "object", "additionalProperties": vals}, nil
	case "StructType":
		return g.structDoc(inspect.TypeFields(te))
	case "InterfaceType":
		if len(inspect.TypeFields(te)) == 0 {
			return map[string]any{
				"type":                 "object",
				"additionalProperties": true,
				"description":          "any (interface{})",
			}, nil
		}
		return nil, errors.New("unsupported non-empty interface type")
	case "IndexExpr", "IndexListExpr":
		// a generic instantiation F[A]: the base leads Children — the
		// walk reaches the generic decl; type arguments are NOT
		// substituted (the same boundary convert-define documents).
		return g.schemaFor(inspect.Children(te)[0])
	case "Ident", "SelectorExpr":
		sid := inspect.SymbolID(te)
		if sid == nil {
			return nil, fmt.Errorf("unresolvable type expression %q", te.Text)
		}
		if sid.PackagePath == ":builtin:" {
			return builtinDoc(sid.Name)
		}
		return g.follow(sid.PackagePath, sid.Name)
	case "FuncType", "ChanType", "Ellipsis", "UnaryExpr", "BinaryExpr":
		return nil, fmt.Errorf("unsupported type shape %q (%s)", te.Text, te.Kind)
	}
	return nil, fmt.Errorf("unsupported type expression %q (%s)", te.Text, te.Kind)
}

// follow renders the schema for a named symbol — the hook points in
// order: plugin overrides, the already-seen $defs table, the stdlib
// cutoff, then the decl walk itself.
func (g *generator) follow(pkgPath, name string) (map[string]any, error) {
	key := pkgPath + "." + name
	if frag, ok := g.cfg.Overrides[key]; ok {
		doc, ok := frag.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("override for %s is %T, want a schema object", key, frag)
		}
		// copy: callers may write field docs/override keys into the
		// fragment — mutating the shared table would leak those keys
		// into every other use of the same override.
		out := make(map[string]any, len(doc))
		for k, v := range doc {
			out[k] = v
		}
		return out, nil
	}
	if name0, ok := g.seen[key]; ok {
		g.used[name0]++
		return map[string]any{"$ref": "#/$defs/" + name0}, nil
	}
	pkg := inspect.PackageOf(pkgPath)
	if g.stdlib == "walk" && (pkg == nil || inspect.Standard(pkg)) {
		// a bound path answers PackageOf with the host package whose
		// index is empty — SourceOf builds the source index behind the
		// bound shadow (and falls back to PackageOf when unbound).
		pkg = inspect.SourceOf(pkgPath)
	}
	if pkg == nil {
		warn("could not resolve package %q — emitting a leaf for %s", pkgPath, key)
		return leafDoc("unresolvable type " + key), nil
	}
	if g.stdlib == "leaf" && inspect.Standard(pkg) {
		return leafDoc("stdlib type " + key + " (walk stopped)"), nil
	}
	d := inspect.Symbol(pkg, name)
	if d == nil {
		// a bound package answers host pseudo-decls for TypeDef-valued
		// members; a miss here means the name is not a type at all.
		warn("no type %s in %s — emitting a leaf", name, pkgPath)
		return leafDoc("unresolvable type " + key), nil
	}
	if inspect.Kind(d) == "host" {
		return leafDoc("bound host type " + key), nil
	}
	defName, err := g.defOf(d)
	if err != nil {
		return nil, err
	}
	return map[string]any{"$ref": "#/$defs/" + defName}, nil
}

// fieldDoc/declDoc trim a doc comment and run the plugins'
// description-filter functions. The hook receives the field/decl
// reference itself, so a filter can read Names, Tag, or Pos rather
// than re-deriving the symbol from the text.
func (g *generator) fieldDoc(f *inspect.Field, raw string) string {
	s := strings.TrimSpace(raw)
	for _, fn := range g.cfg.DescFields {
		s = fn(f, s)
	}
	return s
}

func (g *generator) declDoc(d *inspect.Decl, raw string) string {
	s := strings.TrimSpace(raw)
	for _, fn := range g.cfg.DescDecls {
		s = fn(d, s)
	}
	return s
}

func leafDoc(why string) map[string]any {
	return map[string]any{"description": why}
}

func builtinDoc(name string) (map[string]any, error) {
	switch name {
	case "bool":
		return map[string]any{"type": "boolean"}, nil
	case "int", "int8", "int16", "int32", "int64":
		return map[string]any{"type": "integer"}, nil
	case "rune":
		return map[string]any{"type": "integer"}, nil
	case "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "byte":
		return map[string]any{"type": "integer", "minimum": int64(0)}, nil
	case "float32", "float64", "complex64", "complex128":
		return map[string]any{"type": "number"}, nil
	case "string":
		return map[string]any{"type": "string"}, nil
	case "any":
		return map[string]any{
			"type":                 "object",
			"additionalProperties": true,
			"description":          "any (interface{})",
		}, nil
	}
	return nil, fmt.Errorf("unsupported builtin type %q", name)
}

// structDoc renders one struct field set: json tag names, required
// rules, doc comments, and embedded-field flattening.
func (g *generator) structDoc(fs []*inspect.Field) (map[string]any, error) {
	props := map[string]any{}
	required := map[string]bool{}
	if err := g.addFields(fs, props, required, map[string]bool{}, 0, true); err != nil {
		return nil, err
	}
	doc := map[string]any{
		"type":                 "object",
		"additionalProperties": g.loose,
		"properties":           props,
	}
	if len(required) > 0 {
		var names []any
		for n := range required {
			names = append(names, n)
		}
		sort.Slice(names, func(i, j int) bool { return names[i].(string) < names[j].(string) })
		doc["required"] = names
	}
	return doc, nil
}

// addFields merges one field list into props/required. Named fields
// register first so a promoted field can never shadow an explicit one
// (encoding/json's shallower-wins rule); depth>0 marks promoted fields
// — they only write names not already taken, matching first-wins at
// the same depth (Go drops both on ambiguity; approximation noted).
// reqOK is false once the flatten path crossed a *pointer* embed: a nil
// embedded pointer omits every promoted field, so none are required.
func (g *generator) addFields(fs []*inspect.Field, props map[string]any, required map[string]bool, embedding map[string]bool, depth int, reqOK bool) error {
	var embeds []*inspect.Field
	for _, f := range fs {
		name, named, skip := fieldJSONName(f)
		if skip {
			continue
		}
		if f.Embedded {
			embeds = append(embeds, f)
			continue
		}
		// A, B string: every declared name is its own property.
		for _, goName := range f.Names {
			if !exported(goName) {
				continue
			}
			propName := goName
			if named {
				propName = name
			}
			if depth > 0 {
				if _, taken := props[propName]; taken {
					continue
				}
			}
			fd, err := g.schemaFor(f.Type)
			if err != nil {
				return fmt.Errorf("field %s: %w", goName, err)
			}
			g.applyFieldDoc(fd, f, goName)
			if err := g.applyOverrideTag(fd, f); err != nil {
				return err
			}
			props[propName] = fd
			if reqOK && wantsRequired(f) {
				required[propName] = true
			}
		}
	}
	for _, f := range embeds {
		name, named, _ := fieldJSONName(f)
		if err := g.addEmbedded(f, name, named, props, required, embedding, depth, reqOK); err != nil {
			return err
		}
	}
	return nil
}

// addEmbedded flattens an embedded field's properties inline when it
// resolves to a struct decl — JSON's promoted-field rule. A tagged
// embed keeps its own name; a recursive embed (seen mid-flatten)
// degrades to a named $ref so cycles terminate.
func (g *generator) addEmbedded(f *inspect.Field, name string, named bool, props map[string]any, required map[string]bool, embedding map[string]bool, depth int, reqOK bool) error {
	te := f.Type
	ptr := false
	for te.Kind == "StarExpr" {
		ptr = true
		te = inspect.UnRef(te)
	}
	sid := inspect.SymbolID(te)
	// put keeps promoted writes from shadowing a shallower property —
	// at depth>0 an existing key always wins (encoding/json's rule).
	put := func(k string, v map[string]any) {
		if depth > 0 {
			if _, taken := props[k]; taken {
				return
			}
		}
		props[k] = v
	}
	if named {
		fd, err := g.schemaFor(f.Type)
		if err != nil {
			return fmt.Errorf("embedded field %s: %w", name, err)
		}
		put(name, fd)
		if reqOK && wantsRequired(f) {
			required[name] = true
		}
		return nil
	}
	if sid == nil || sid.PackagePath == ":builtin:" {
		// non-struct embeds marshal under the type name (encoding/json).
		fd, err := g.schemaFor(f.Type)
		if err != nil {
			return err
		}
		put(te.Text, fd)
		return nil
	}
	key := sid.PackagePath + "." + sid.Name
	if embedding[key] {
		// A{*B}, B{*A}: an unbounded flatten — degrade to a named ref.
		fd, err := g.schemaFor(f.Type)
		if err != nil {
			return err
		}
		put(sid.Name, fd)
		return nil
	}
	// follow the same leaf policy as named fields
	pkg := inspect.PackageOf(sid.PackagePath)
	if pkg == nil {
		warn("could not resolve package %q — embedded %s left as named leaf", sid.PackagePath, key)
		put(sid.Name, leafDoc("unresolvable type "+key))
		return nil
	}
	if g.stdlib == "leaf" && inspect.Standard(pkg) {
		put(sid.Name, leafDoc("stdlib type "+key+" (walk stopped)"))
		return nil
	}
	d := inspect.Symbol(pkg, sid.Name)
	if d == nil || inspect.Kind(d) != "type" || inspect.Kind(d) == "host" {
		fd, err := g.schemaFor(f.Type)
		if err != nil {
			return err
		}
		put(sid.Name, fd)
		return nil
	}
	def := inspect.Def(d)
	if def == nil || def.Kind != "StructType" {
		fd, err := g.schemaFor(f.Type)
		if err != nil {
			return err
		}
		put(sid.Name, fd)
		return nil
	}
	efs := inspect.TypeFields(def)
	embedding[key] = true
	defer delete(embedding, key)
	return g.addFields(efs, props, required, embedding, depth+1, reqOK && !ptr)
}

func (g *generator) applyFieldDoc(fd map[string]any, f *inspect.Field, name string) {
	if f.Doc != "" {
		fd["description"] = g.fieldDoc(f, f.Doc)
	}
}

// applyOverrideTag merges the jsonschema-override struct tag: literal
// keys land on the field doc, "required" feeds the required set.
func (g *generator) applyOverrideTag(fd map[string]any, f *inspect.Field) error {
	tag := reflect.StructTag(f.Tag)
	raw, ok := tag.Lookup("jsonschema-override")
	if !ok || raw == "" {
		return nil
	}
	for _, pair := range strings.Split(raw, ";") {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			return fmt.Errorf("field %s: malformed jsonschema-override %q", f.Names[0], raw)
		}
		k, v := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
		if k == "required" {
			continue // required is computed by wantsRequired below
		}
		fd[k] = coerceScalar(v)
	}
	return nil
}

// fieldJSONName resolves a field's property name from its struct tag:
// json wins, then yaml, then toml. skip reports `json:"-"`; named
// reports the tag carried an explicit name ("" name keeps the Go name).
func fieldJSONName(f *inspect.Field) (name string, named bool, skip bool) {
	tag := reflect.StructTag(f.Tag)
	for _, key := range []string{"json", "yaml", "toml"} {
		if tv, ok := tag.Lookup(key); ok {
			parts := strings.Split(tv, ",")
			if parts[0] == "-" {
				return "", false, true
			}
			if parts[0] != "" {
				return parts[0], true, false
			}
			return "", false, false
		}
	}
	return "", false, false
}

// wantsRequired applies the required rules: pointer and omitempty mark
// optional, a `required:"bool"` tag overrides both.
func wantsRequired(f *inspect.Field) bool {
	required := f.Type.Kind != "StarExpr"
	tag := reflect.StructTag(f.Tag)
	for _, key := range []string{"json", "yaml", "toml"} {
		if tv, ok := tag.Lookup(key); ok {
			for _, opt := range strings.Split(tv, ",")[1:] {
				if strings.TrimSpace(opt) == "omitempty" {
					required = false
				}
			}
			break
		}
	}
	if v, ok := tag.Lookup("required"); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			required = b
		}
	}
	return required
}

// coerceScalar turns a jsonschema-override value into its JSON scalar
// when it is one — "1" stays "1" only when it is meant as a string.
func coerceScalar(v string) any {
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n
	}
	if n, err := strconv.ParseFloat(v, 64); err == nil {
		return n
	}
	if b, err := strconv.ParseBool(v); err == nil {
		return b
	}
	if v == "null" {
		return nil
	}
	return v
}

func exported(name string) bool {
	if name == "" {
		return false
	}
	c := name[0]
	return 'A' <= c && c <= 'Z'
}
