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
	"unicode"

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
	// The modes resolve different namespaces — a directory vs an import
	// path — so pick the matching loader before loading; probing the
	// wrong one first trapped on the -pkg target string. The Lookup*
	// twins answer nil on a miss, making the "could not load" check
	// below live instead of dead.
	load := inspect.LookupDirOf
	if pkgMode {
		load = inspect.LookupPackageOf
	}
	pkg := load(target)
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
		d := inspect.LookupSymbol(pkg, typeName)
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

// loadPlugins loops the plugin dirs and invokes every exported func
// whose shape is `func(c *extschema.Config)` — detection is by
// signature, not name, so a plugin's entry point is whatever it calls
// itself. All calls happen in this same VM via inspect.Value; a dir
// with no matching func is a warning, not a failure.
func (g *generator) loadPlugins(dirs []string) error {
	for _, dir := range dirs {
		pp := inspect.LookupDirOf(dir)
		if pp == nil {
			return fmt.Errorf("extract-schema: could not load plugin %q", dir)
		}
		hooked := false
		for _, d := range configurers(inspect.Decls(pp)) {
			inspect.Value(pp, d.Name).(func(*extschema.Config))(g.cfg)
			hooked = true
		}
		if !hooked {
			warn("plugin %s exposes no func(*extschema.Config)", dir)
		}
	}
	return nil
}

// configurers lists the exported funcs shaped like plugin entry
// points: plain functions (no receiver) taking exactly one parameter
// of type *extschema.Config.
func configurers(ds []*inspect.Decl) []*inspect.Decl {
	var out []*inspect.Decl
	for _, d := range ds {
		if inspect.Kind(d) != "func" || !isExported(d.Name) {
			continue
		}
		sig := inspect.Signature(d)
		if sig == nil || sig.Recv != nil {
			continue
		}
		// arity, not field groups: `func(a, b *extschema.Config)` is one
		// field carrying two names — NumParams counts the names.
		if sig.NumParams() != 1 || sig.NumResults() != 0 {
			continue
		}
		ps := sig.ParamFields()
		if len(ps) != 1 {
			continue
		}
		// The param spells *extschema.Config: unwrap the pointer,
		// resolve to the decl, and compare package path + name rather
		// than the spelling — a renamed import or a `Config` spelled
		// differently still counts.
		pt := ps[0].Type
		if pt != nil && pt.Kind == "StarExpr" {
			rd := inspect.Resolve(inspect.UnRef(pt))
			if rd != nil && rd.Name == "Config" && inspect.Path(rd.Package) == "github.com/podhmo/minigo/examples/extract-schema/extschema" {
				out = append(out, d)
			}
		}
	}
	return out
}

// isExported reports whether a name is exported — Go's rule is
// unicode.IsUpper on the first rune, so `Ändern` counts too.
func isExported(name string) bool {
	for _, r := range name {
		return unicode.IsUpper(r)
	}
	return false
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
		elem := inspect.Children(te)[0]
		if isByteSlice(te, elem) {
			// encoding/json marshals []byte as a base64 string —
			// the one slice whose wire shape is not an array.
			return map[string]any{"type": "string", "contentEncoding": "base64"}, nil
		}
		items, err := g.schemaFor(elem)
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
		if sid.PackagePath == inspect.BuiltinPackagePath {
			return builtinDoc(sid.Name)
		}
		if sid.PackagePath == inspect.TypeParamPackagePath {
			// bound by the enclosing generic decl — no substitution
			// surface, so the parameter degrades to a leaf.
			return leafDoc("type parameter " + sid.Name), nil
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
	// the soft twin: a hard PackageOf traps on paths the module graph
	// cannot name — notably the "<dir>" pseudo-path a dir-loaded
	// package outside the engine's module spells — while the Lookup
	// form answers nil there and the check below degrades to a leaf.
	pkg := inspect.LookupPackageOf(pkgPath)
	if g.stdlib == "walk" && pkg != nil && inspect.Standard(pkg) {
		// a bound path answers PackageOf with the host package whose
		// index is empty — SourceOf builds the source index behind the
		// bound shadow (and falls back to PackageOf when unbound).
		// A nil lookup stays on the leaf path: SourceOf would only
		// re-fail the same resolve the Lookup already missed.
		pkg = inspect.SourceOf(pkgPath)
	}
	if pkg == nil {
		warn("could not resolve package %q — emitting a leaf for %s", pkgPath, key)
		return leafDoc("unresolvable type " + key), nil
	}
	if g.stdlib == "leaf" && inspect.Standard(pkg) {
		return leafDoc("stdlib type " + key + " (walk stopped)"), nil
	}
	d := inspect.LookupSymbol(pkg, name)
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
	depths := map[string]int{}
	ambig := map[string]int{}
	if err := g.addFields(fs, props, required, depths, ambig, map[string]bool{}, 0, true); err != nil {
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

// putProp writes one property honouring encoding/json's depth rules:
// strictly shallower replaces deeper, deeper yields, and an equal
// depth marks the name ambiguous — the prop is dropped and deeper or
// equal-depth candidates stay rejected (ambig records the poisoned
// depth). required entries follow the surviving write. Returns
// whether the property was written.
func putProp(props map[string]any, required map[string]bool, depths, ambig map[string]int, k string, v map[string]any, atDepth int) bool {
	if ad, ok := ambig[k]; ok && atDepth >= ad {
		return false
	}
	if cur, ok := depths[k]; ok {
		if atDepth > cur {
			return false
		}
		if atDepth == cur {
			delete(props, k)
			delete(required, k)
			ambig[k] = atDepth
			return false
		}
		delete(required, k) // shallower replaces: drop the deeper write
	}
	depths[k] = atDepth
	props[k] = v
	return true
}

// addFields merges one field list into props/required. Named fields
// register first so a promoted field can never shadow an explicit one;
// every write then goes through putProp, which applies encoding/json's
// promotion rules: a strictly shallower property replaces a deeper one
// (deep embeds lose to later, shallower siblings), and an equal-depth
// collision marks the name ambiguous — Go drops *both*. depths/ambig
// carry the bookkeeping across the recursive calls. reqOK is false
// once the flatten path crossed a *pointer* embed: a nil embedded
// pointer omits every promoted field, so none are required.
func (g *generator) addFields(fs []*inspect.Field, props map[string]any, required map[string]bool, depths, ambig map[string]int, embedding map[string]bool, depth int, reqOK bool) error {
	var embeds []taggedField
	for _, f := range fs {
		ft := parseFieldTag(f)
		if ft.skip {
			continue
		}
		if f.Embedded {
			embeds = append(embeds, taggedField{f, ft})
			continue
		}
		// A, B string: every declared name is its own property.
		for _, goName := range f.Names {
			if !isExported(goName) {
				continue
			}
			propName := goName
			if ft.named {
				propName = ft.name
			}
			fd, err := g.schemaFor(f.Type)
			if err != nil {
				return fmt.Errorf("field %s: %w", goName, err)
			}
			g.applyFieldDoc(fd, f, goName)
			applyStringOption(fd, ft.opts)
			if err := g.applyOverrideTag(fd, ft, f); err != nil {
				return err
			}
			if putProp(props, required, depths, ambig, propName, fd, depth) && reqOK && ft.required {
				required[propName] = true
			}
		}
	}
	for _, tf := range embeds {
		if err := g.addEmbedded(tf.f, tf.tag, props, required, depths, ambig, embedding, depth, reqOK); err != nil {
			return err
		}
	}
	return nil
}

// addEmbedded flattens an embedded field's properties inline when it
// resolves to a struct decl — JSON's promoted-field rule. A tagged
// embed keeps its own name; a recursive embed (seen mid-flatten)
// drops the subtree whole, matching encoding/json's cycle cut.
func (g *generator) addEmbedded(f *inspect.Field, ft fieldTag, props map[string]any, required map[string]bool, depths, ambig map[string]int, embedding map[string]bool, depth int, reqOK bool) error {
	te := f.Type
	ptr := false
	for te.Kind == "StarExpr" {
		ptr = true
		te = inspect.UnRef(te)
	}
	sid := inspect.SymbolID(te)
	// put writes a named/tag embed's own property at this level —
	// putProp keeps it from shadowing a shallower winner.
	put := func(k string, v map[string]any) {
		putProp(props, required, depths, ambig, k, v, depth)
	}
	if ft.named {
		fd, err := g.schemaFor(f.Type)
		if err != nil {
			return fmt.Errorf("embedded field %s: %w", ft.name, err)
		}
		put(ft.name, fd)
		if reqOK && ft.required {
			required[ft.name] = true
		}
		return nil
	}
	if sid == nil || inspect.NoDeclPath(sid.PackagePath) {
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
		// A{*B}, B{*A}: an unbounded flatten — encoding/json drops the
		// subtree rather than inventing a type-named property.
		return nil
	}
	// follow the same leaf policy as named fields — the soft Lookup
	// twin so a "<dir>" pseudo-path sid degrades instead of trapping.
	pkg := inspect.LookupPackageOf(sid.PackagePath)
	if pkg == nil {
		warn("could not resolve package %q — embedded %s left as named leaf", sid.PackagePath, key)
		put(sid.Name, leafDoc("unresolvable type "+key))
		return nil
	}
	if g.stdlib == "leaf" && inspect.Standard(pkg) {
		put(sid.Name, leafDoc("stdlib type "+key+" (walk stopped)"))
		return nil
	}
	d := inspect.LookupSymbol(pkg, sid.Name)
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
	return g.addFields(efs, props, required, depths, ambig, embedding, depth+1, reqOK && !ptr)
}

func (g *generator) applyFieldDoc(fd map[string]any, f *inspect.Field, name string) {
	if f.Doc != "" {
		fd["description"] = g.fieldDoc(f, f.Doc)
	}
}

// applyOverrideTag merges the jsonschema-override struct tag: literal
// keys land on the field doc, "required" feeds the required set.
func (g *generator) applyOverrideTag(fd map[string]any, ft fieldTag, f *inspect.Field) error {
	raw, ok := ft.tag.Lookup("jsonschema-override")
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
			continue // required is folded into fieldTag.required
		}
		fd[k] = coerceScalar(v)
	}
	return nil
}

// fieldTag is one field's struct tag parsed once and shared by the
// name/required/override readers. Tag precedence layers: naming takes
// the first present key of json > yaml > toml — its name and comma
// options win together; skip is that key spelling "-"; required
// layers a pointer check under the winning key's omitempty, then a
// `required:"bool"` tag overrides both, and a `required=` pair inside
// jsonschema-override is the outermost patch; the remaining
// jsonschema-override pairs merge wholesale in applyOverrideTag.
type fieldTag struct {
	tag      reflect.StructTag
	name     string   // the winning key's name ("" keeps the Go name)
	opts     []string // the winning key's comma options (`,omitempty`, `,string`)
	named    bool     // the winning key spelled an explicit name
	skip     bool     // the winning key spelled "-"
	required bool     // non-pointer && !omitempty, unless required:"bool" says otherwise
}

// taggedField pairs a field with its parsed tag so an embed deferred
// to the flatten pass keeps the tag its first read computed.
type taggedField struct {
	f   *inspect.Field
	tag fieldTag
}

// parseFieldTag reads a field's struct tag once: the first present of
// json/yaml/toml decides name + opts + skip, and required folds in the
// pointer rule, omitempty and the `required:"bool"` override.
func parseFieldTag(f *inspect.Field) fieldTag {
	var ft fieldTag
	ft.tag = reflect.StructTag(f.Tag)
	for _, key := range []string{"json", "yaml", "toml"} {
		tv, ok := ft.tag.Lookup(key)
		if !ok {
			continue
		}
		parts := strings.Split(tv, ",")
		if parts[0] == "-" {
			ft.skip = true
		} else {
			ft.name = parts[0]
			ft.named = parts[0] != ""
		}
		ft.opts = parts[1:]
		break
	}
	ft.required = f.Type.Kind != "StarExpr"
	for _, opt := range ft.opts {
		if strings.TrimSpace(opt) == "omitempty" {
			ft.required = false
		}
	}
	if v, ok := ft.tag.Lookup("required"); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			ft.required = b
		}
	}
	// the override tag is the outermost patch layer: required= in it
	// beats both the shape default and a required:"" tag.
	if raw, ok := ft.tag.Lookup("jsonschema-override"); ok {
		for _, pair := range strings.Split(raw, ";") {
			kv := strings.SplitN(pair, "=", 2)
			if len(kv) != 2 || strings.TrimSpace(kv[0]) != "required" {
				continue
			}
			if b, err := strconv.ParseBool(strings.TrimSpace(kv[1])); err == nil {
				ft.required = b
			}
		}
	}
	return ft
}

// applyStringOption honours encoding/json's ,string tag option: scalar
// values marshal as quoted JSON strings, so the schema type degrades
// to "string". Numeric-only bounds drop — a bound that checked a
// number cannot check its quoted form (minLength would be a different
// rule than minimum: "10" is longer than "9" yet still fails
// minimum=10) — and enum/const members rewrite to the quoted scalars
// the wire carries, so a "1" wire value still validates.
func applyStringOption(fd map[string]any, opts []string) {
	for _, o := range opts {
		if strings.TrimSpace(o) != "string" {
			continue
		}
		switch fd["type"] {
		case "string", "integer", "number", "boolean":
			fd["type"] = "string"
			for _, k := range []string{
				"minimum", "maximum",
				"exclusiveMinimum", "exclusiveMaximum",
				"multipleOf",
			} {
				delete(fd, k)
			}
			if v, ok := fd["enum"]; ok {
				fd["enum"] = quoteEnum(v)
			}
			if v, ok := fd["const"]; ok {
				fd["const"] = quoteScalar(v)
			}
		}
		return
	}
}

// quoteEnum rewrites a schema enum list member-wise for ,string —
// each member keeps constraining, spelled the way the wire spells it.
func quoteEnum(v any) any {
	xs, ok := v.([]any)
	if !ok {
		return v
	}
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = quoteScalar(x)
	}
	return out
}

// quoteScalar renders one scalar in its ,string wire form: encoding/json
// quotes ints, bools, and floats inside the string ("1", "true",
// "1.5" — the shortest float repr), a string member double-encodes
// ("a" becomes the JSON text "a"), and null stays literal null —
// ,string does not quote nil.
func quoteScalar(v any) any {
	switch x := v.(type) {
	case string:
		return strconv.Quote(x)
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	}
	return v
}

// coerceScalar turns a jsonschema-override value into its JSON scalar
// when it is one — "1" stays "1" only when it is meant as a string.
func coerceScalar(v string) any {
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n
	}
	if n, err := strconv.ParseFloat(v, 64); err == nil {
		// NaN/Inf parse fine but are not JSON — keep the literal.
		if n != n || n > 1.7976931348623157e308 || n < -1.7976931348623157e308 {
			return v
		}
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

// isByteSlice reports whether te spells a slice whose element kind is
// uint8 — []byte, []uint8, or a named byte type (`type Byte byte`):
// encoding/json marshals all of them as base64 strings. The chase
// follows each named element to its declared TypeExpr until a builtin
// sid answers, an unresolvable or non-type decl stops it, a composite
// def (type Bytes []byte — element kind is slice, not uint8) drops
// out, or the depth bound cuts a pathological chain. Fixed arrays
// keep the array shape; only the slice form marshals as base64.
//
// The element type's own method set escapes the shortcut: gc's
// byteSliceEncoder fires only when neither the elem type nor its
// pointer implements json.Marshaler or encoding.TextMarshaler —
// exactly the names *T's flattened method set carries. The check
// applies at the first hop only: a `type B A` chain does not carry
// methods (gc marshals []B as base64 even when A marshals itself).
func isByteSlice(te, elem *inspect.TypeExpr) bool {
	if !strings.HasPrefix(te.Text, "[]") {
		return false
	}
	sid := inspect.SymbolID(elem)
	for depth := 0; sid != nil && depth < 8; depth++ {
		if sid.PackagePath == inspect.BuiltinPackagePath {
			return sid.Name == "byte" || sid.Name == "uint8"
		}
		pkg := inspect.LookupPackageOf(sid.PackagePath)
		if pkg == nil {
			return false
		}
		d := inspect.LookupSymbol(pkg, sid.Name)
		if d == nil || inspect.Kind(d) != "type" {
			return false
		}
		if depth == 0 {
			for _, m := range inspect.MethodSet(d) {
				if m.Name == "MarshalJSON" || m.Name == "MarshalText" {
					return false
				}
			}
		}
		def := inspect.Def(d)
		if def == nil {
			return false
		}
		sid = inspect.SymbolID(def)
	}
	return false
}
