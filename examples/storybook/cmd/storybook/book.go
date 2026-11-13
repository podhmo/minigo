package main

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/resolve"
)

// Book is one package's set of stories.
type Book struct {
	Dir string   // package directory
	Tag string   // build tag guarding the story files
	Src []string // packages to interpret from source
}

// Story is a story function found in a tagged file.
type Story struct {
	Name string // StoryXxx
	Doc  string // first line of the doc comment
	Pos  string // file:line
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
			if !isStoryShape(fd.Type) {
				return nil, fmt.Errorf("%s:%d: %s must be func()", pos.Filename, pos.Line, fd.Name.Name)
			}
			doc, _, _ := strings.Cut(strings.TrimSpace(fd.Doc.Text()), "\n")
			stories = append(stories, Story{Name: fd.Name.Name, Doc: doc, Pos: fmt.Sprintf("%s:%d", pos.Filename, pos.Line)})
		}
	}
	if len(stories) == 0 {
		return nil, fmt.Errorf("%s: no Story funcs in files guarded by //go:build %s", b.Dir, b.Tag)
	}
	slices.SortFunc(stories, func(x, y Story) int { return strings.Compare(x.Name, y.Name) })
	return stories, nil
}

// isStoryShape accepts only func() with no results or type parameters.
func isStoryShape(ft *ast.FuncType) bool {
	return (ft.TypeParams == nil || len(ft.TypeParams.List) == 0) &&
		(ft.Params == nil || len(ft.Params.List) == 0) &&
		(ft.Results == nil || len(ft.Results.List) == 0)
}

// Render runs one story in a fresh engine, so an edit to the package
// shows up on the next call, and captures its stdout as HTML. Stories own
// their requests and fixtures; the helper injects no HTTP values.
func (b *Book) Render(ctx context.Context, name string) ([]byte, error) {
	stories, err := b.Stories()
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(stories, func(s Story) bool { return s.Name == name }) {
		return nil, fmt.Errorf("%s: not a story", name)
	}
	var output bytes.Buffer
	opts := []minigo.Option{minigo.WithOutput(&output), minigo.WithBuildConfig(resolve.BuildConfig{Tags: []string{b.Tag}})}
	if len(b.Src) > 0 {
		modes := map[string]minigo.PackageMode{}
		for _, p := range b.Src {
			modes[p] = minigo.ModeSource
		}
		opts = append(opts, minigo.WithPackageModes(modes))
	}
	dir, err := filepath.Abs(b.Dir)
	if err != nil {
		return nil, err
	}
	e := minigo.NewEngine(dir, opts...)
	pkg, err := e.Package(ctx, dir)
	if err != nil {
		return nil, err
	}
	if _, err := e.Call(ctx, pkg, name); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return output.Bytes(), nil
}
