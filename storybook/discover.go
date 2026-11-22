// Discovery: scanning a package directory for story funcs. [Discover]
// lists the ones the build tag selects; [Excluded] lists the
// Story-prefixed funcs it skips so a missing //go:build line never
// fails silently.
package storybook

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
)

// StoryKind distinguishes the two story shapes.
type StoryKind int

const (
	// KindPrint is `func StoryXxx()` writing HTML to stdout.
	KindPrint StoryKind = iota
	// KindHTTP is `func StoryHttpXxx(w http.ResponseWriter, r *http.Request)` —
	// handler-shaped, so the story exercises real handlers and the recorder
	// captures status code and headers too.
	KindHTTP
)

// Story is a story function found in a tagged file.
type Story struct {
	Name string // StoryXxx or StoryHttpXxx
	Doc  string // first line of the doc comment
	Pos  string // file:line
	Kind StoryKind
	// Group names the render function the story drives — by convention
	// the last non-builtin unqualified call in the story body, e.g.
	// RenderUser. A `//storybook:group <name>` line in the story's doc
	// comment overrides the heuristic for stories that must end on a
	// helper instead. An index uses it to lay a template's data-state
	// variants out together; "" when neither source names a group
	// (handler stories call methods, so they stay ungrouped).
	Group string
	// GroupInferred reports that Group came from the last-call
	// heuristic rather than an explicit //storybook:group marker — the
	// heuristic reads the body, so a trailing helper call silently
	// steals the group. Consumers can surface inferred groups as a
	// "probably right, declare it to be sure" hint.
	GroupInferred bool
}

// IsHTTP reports whether the story is the handler-shaped kind.
func (s Story) IsHTTP() bool { return s.Kind == KindHTTP }

// Discover lists the story funcs declared in dir's files that the build
// tag brings in: files the default build excludes but the tagged build
// selects. A Story-prefixed func of any other shape is an error, so a
// typo in the signature does not silently drop the story.
func Discover(dir, tag string) ([]Story, error) {
	stories, _, err := scanDir(dir, tag)
	if err != nil {
		return nil, err
	}
	if len(stories) == 0 {
		return nil, fmt.Errorf("%s: no Story funcs in files guarded by //go:build %s", dir, tag)
	}
	return stories, nil
}

// Excluded lists Story-prefixed funcs that discovery skips: declared in
// files the tag does not select, or in untagged files that join every
// build. They never reach the index, so callers can surface them as a
// "did you mean //go:build <tag>?" warning instead of a silent miss.
// Shapes are not validated — a malformed Story in a skipped file is
// still reported.
func Excluded(dir, tag string) ([]Story, error) {
	_, excluded, err := scanDir(dir, tag)
	return excluded, err
}

// Stories lists the book's story funcs — [Discover] over the book's
// Dir and Tag.
func (b *Book) Stories() ([]Story, error) {
	return Discover(b.Dir, b.Tag)
}

// Excluded lists the book's skipped story funcs — [Excluded] over the
// book's Dir and Tag.
func (b *Book) Excluded() ([]Story, error) {
	return Excluded(b.Dir, b.Tag)
}

// scanDir walks dir's Go files once, splitting Story-prefixed funcs
// into discovered stories and skipped ones.
func scanDir(dir, tag string) (stories, excluded []Story, err error) {
	plain := build.Default
	tagged := build.Default
	tagged.BuildTags = append(slices.Clone(tagged.BuildTags), tag)
	matches, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, nil, err
	}
	fset := token.NewFileSet()
	for _, path := range matches {
		name := filepath.Base(path)
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		inTagged, err := tagged.MatchFile(dir, name)
		if err != nil {
			return nil, nil, err
		}
		inPlain, err := plain.MatchFile(dir, name)
		if err != nil {
			return nil, nil, err
		}
		selected := inTagged && !inPlain
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return nil, nil, err
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || !strings.HasPrefix(fd.Name.Name, "Story") || !fd.Name.IsExported() {
				continue
			}
			pos := fset.Position(fd.Pos())
			kind := KindPrint
			if strings.HasPrefix(fd.Name.Name, "StoryHttp") {
				kind = KindHTTP
			}
			doc, _, _ := strings.Cut(strings.TrimSpace(fd.Doc.Text()), "\n")
			s := Story{Name: fd.Name.Name, Doc: doc, Pos: fmt.Sprintf("%s:%d", pos.Filename, pos.Line), Kind: kind}
			if !selected {
				excluded = append(excluded, s)
				continue
			}
			if kind == KindHTTP {
				if !isHandlerShape(fd.Type) {
					return nil, nil, fmt.Errorf("%s: %s must be func(http.ResponseWriter, *http.Request)", s.Pos, s.Name)
				}
			} else if !isStoryShape(fd.Type) {
				return nil, nil, fmt.Errorf("%s: %s must be func()", s.Pos, s.Name)
			}
			marked, err := markedGroup(fd.Doc)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %s: %w", s.Pos, s.Name, err)
			}
			// Only print stories group: a handler story calling an
			// unqualified helper would otherwise point an index at a
			// grid that cannot frame it.
			switch {
			case marked != "" && kind == KindHTTP:
				return nil, nil, fmt.Errorf("%s: %s: storybook:group is ignored on handler stories; remove the marker", s.Pos, s.Name)
			case marked != "":
				s.Group = marked
			case kind == KindPrint:
				s.Group = storyGroup(fd.Body)
				s.GroupInferred = s.Group != ""
			}
			stories = append(stories, s)
		}
	}
	slices.SortFunc(stories, func(x, y Story) int { return strings.Compare(x.Name, y.Name) })
	slices.SortFunc(excluded, func(x, y Story) int { return strings.Compare(x.Name, y.Name) })
	return stories, excluded, nil
}

// markedGroup reads the explicit group override out of a story's doc
// comment — a line like `//storybook:group RenderUser` — so a story
// that must end on a helper (must(...), defer, a wrapper) can still
// name its render function instead of letting the heuristic steal it.
func markedGroup(doc *ast.CommentGroup) (string, error) {
	if doc == nil {
		return "", nil
	}
	for _, c := range doc.List {
		rest, ok := strings.CutPrefix(c.Text, "//storybook:group")
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimSpace(rest), " ")
		if name == "" {
			return "", fmt.Errorf("//storybook:group needs a render function name")
		}
		return name, nil
	}
	return "", nil
}

// storyGroup reads the render function out of a story body: the last
// call whose callee is a plain identifier and not a builtin. Stories
// conventionally end by calling their render function with one data
// state (RenderUser(&b, u)), so the callee groups the template's
// variants. Selector calls (RenderUserList.Run, fmt.Print) and builtins
// (panic, make) never count.
func storyGroup(body *ast.BlockStmt) string {
	group := ""
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && !builtins[id.Name] {
			group = id.Name
		}
		return true
	})
	return group
}

var builtins = map[string]bool{
	"append": true, "cap": true, "clear": true, "close": true,
	"complex": true, "copy": true, "delete": true, "imag": true,
	"len": true, "make": true, "max": true, "min": true, "new": true,
	"panic": true, "print": true, "println": true, "real": true,
	"recover": true,
}

// isStoryShape accepts only func() with no results or type parameters.
func isStoryShape(ft *ast.FuncType) bool {
	return (ft.TypeParams == nil || len(ft.TypeParams.List) == 0) &&
		(ft.Params == nil || len(ft.Params.List) == 0) &&
		(ft.Results == nil || len(ft.Results.List) == 0)
}

// isHandlerShape accepts func(http.ResponseWriter, *http.Request) — the
// http.HandlerFunc signature, detected structurally so the import alias
// does not matter. Parameter names count: one field declaring two names
// is two parameters, so `func(w, extra http.ResponseWriter, r *Request)`
// is rejected.
func isHandlerShape(ft *ast.FuncType) bool {
	if (ft.TypeParams != nil && len(ft.TypeParams.List) > 0) ||
		(ft.Results != nil && len(ft.Results.List) > 0) ||
		ft.Params == nil || len(ft.Params.List) != 2 {
		return false
	}
	n := 0
	for _, f := range ft.Params.List {
		if len(f.Names) == 0 {
			n++
		} else {
			n += len(f.Names)
		}
	}
	if n != 2 {
		return false
	}
	sel0, ok0 := ft.Params.List[0].Type.(*ast.SelectorExpr)
	star, ok1 := ft.Params.List[1].Type.(*ast.StarExpr)
	if !ok0 || !ok1 {
		return false
	}
	sel1, ok2 := star.X.(*ast.SelectorExpr)
	return ok2 && sel0.Sel.Name == "ResponseWriter" && sel1.Sel.Name == "Request"
}
