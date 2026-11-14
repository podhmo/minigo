package main

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/resolve"
)

// Book is one package's set of stories.
type Book struct {
	Dir       string   // package directory
	Tag       string   // build tag guarding the story files
	Src       []string // packages to interpret from source
	StaticDir string   // directory served under /static/ in -http mode

	once    sync.Once
	base    *minigo.Engine // configured once; each Render forks a session
	dir     string         // absolute Dir, computed with base
	baseErr error
}

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
	// RenderUser. The index uses it to lay a template's data-state
	// variants out together; "" when the body has no such call
	// (handler stories call methods, so they stay ungrouped).
	Group string
}

// IsHTTP reports whether the story is the handler-shaped kind.
func (s Story) IsHTTP() bool { return s.Kind == KindHTTP }

// Page is one rendered story: body plus, for handler-shaped stories, the
// recorded status code and response headers.
type Page struct {
	Status int
	Header map[string][]string
	Body   []byte
}

// Stories lists the story funcs declared in files that the build tag
// brings in: files the default build excludes but the tagged build
// selects. A Story-prefixed func of any other shape is an error, so a
// typo in the signature does not silently drop the story.
func (b *Book) Stories() ([]Story, error) {
	plain := build.Default
	tagged := build.Default
	tagged.BuildTags = append(slices.Clone(tagged.BuildTags), b.Tag)
	matches, err := filepath.Glob(filepath.Join(b.Dir, "*.go"))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var stories []Story
	for _, path := range matches {
		name := filepath.Base(path)
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		inTagged, err := tagged.MatchFile(b.Dir, name)
		if err != nil {
			return nil, err
		}
		inPlain, err := plain.MatchFile(b.Dir, name)
		if err != nil {
			return nil, err
		}
		if !inTagged || inPlain {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || !strings.HasPrefix(fd.Name.Name, "Story") || !fd.Name.IsExported() {
				continue
			}
			pos := fset.Position(fd.Pos())
			kind := KindPrint
			if strings.HasPrefix(fd.Name.Name, "StoryHttp") {
				if !isHandlerShape(fd.Type) {
					return nil, fmt.Errorf("%s:%d: %s must be func(http.ResponseWriter, *http.Request)", pos.Filename, pos.Line, fd.Name.Name)
				}
				kind = KindHTTP
			} else if !isStoryShape(fd.Type) {
				return nil, fmt.Errorf("%s:%d: %s must be func()", pos.Filename, pos.Line, fd.Name.Name)
			}
			doc, _, _ := strings.Cut(strings.TrimSpace(fd.Doc.Text()), "\n")
			// Only print stories group: a handler story calling an
			// unqualified helper would otherwise point the index at a
			// grid that cannot frame it.
			group := ""
			if kind == KindPrint {
				group = storyGroup(fd.Body)
			}
			stories = append(stories, Story{Name: fd.Name.Name, Doc: doc, Pos: fmt.Sprintf("%s:%d", pos.Filename, pos.Line), Kind: kind, Group: group})
		}
	}
	if len(stories) == 0 {
		return nil, fmt.Errorf("%s: no Story funcs in files guarded by //go:build %s", b.Dir, b.Tag)
	}
	slices.SortFunc(stories, func(x, y Story) int { return strings.Compare(x.Name, y.Name) })
	return stories, nil
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

// Render runs one story in a session forked from the book's base engine:
// the session keeps the configuration and host bindings but starts with an
// empty package cache, so an edit to the package — template, Go code, or an
// //go:embed'ed file — shows up on the next call while bound packages stay
// loaded. Stories own their fixtures; the helper injects no HTTP values.
// Print stories answer Status 200 with an empty Header; handler stories
// report the recorder's result.
func (b *Book) Render(ctx context.Context, name string) (*Page, error) {
	stories, err := b.Stories()
	if err != nil {
		return nil, err
	}
	kind := KindHTTP + 1
	for _, st := range stories {
		if st.Name == name {
			kind = st.Kind
			break
		}
	}
	if kind > KindHTTP {
		return nil, fmt.Errorf("%s: not a story", name)
	}
	base, dir, err := b.baseEngine()
	if err != nil {
		return nil, err
	}
	s := base.NewSession()
	var output bytes.Buffer
	minigo.WithOutput(&output)(s)
	pkg, err := s.Package(ctx, dir)
	if err != nil {
		return nil, err
	}
	if kind == KindHTTP {
		// Handler-shaped story: drive it through a recorder so the
		// response status and headers are captured, not just the body.
		// The request is a plain GET /; stories wanting a specific path
		// build their own with httptest.NewRequest inside.
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if _, err := s.Call(ctx, pkg, name, rec, req); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		res := rec.Result()
		// the handler writes to the recorder, not to stdout: the page
		// body is the recorder's buffer, not the captured print output.
		return &Page{Status: res.StatusCode, Header: res.Header, Body: rec.Body.Bytes()}, nil
	}
	if _, err := s.Call(ctx, pkg, name); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return &Page{Status: http.StatusOK, Body: output.Bytes()}, nil
}

// baseEngine lazily builds the long-lived engine all renders fork from.
// Build-time options (tags, package modes) live here; the per-request
// output buffer is installed on each session instead. An application that
// needs host binds — faking a driver import, binding a third-party
// package so it stays compiled — registers them on the base engine here,
// once, and every session inherits them (Engine.Bind; rename the bound
// package via BoundOf when the declared package name differs from the
// last import-path element).
func (b *Book) baseEngine() (*minigo.Engine, string, error) {
	// the handler can serve concurrent first requests, so the build is
	// serialized: otherwise b.base could be observed before b.dir.
	b.once.Do(func() {
		dir, err := filepath.Abs(b.Dir)
		if err != nil {
			b.baseErr = err
			return
		}
		opts := []minigo.Option{minigo.WithBuildConfig(resolve.BuildConfig{Tags: []string{b.Tag}})}
		if len(b.Src) > 0 {
			modes := map[string]minigo.PackageMode{}
			for _, p := range b.Src {
				modes[p] = minigo.ModeSource
			}
			opts = append(opts, minigo.WithPackageModes(modes))
		}
		b.base, b.dir = minigo.NewEngine(dir, opts...), dir
	})
	return b.base, b.dir, b.baseErr
}
