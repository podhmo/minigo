package main

import (
	"net/http"
	"path/filepath"

	"github.com/podhmo/minigo/storybook"
)

// Book is the shared storybook.Book plus the command's static-asset
// serving root: story discovery, the base-engine/session render loop,
// and the recorder drive all live in the shared package.
type Book struct {
	*storybook.Book
	StaticDir string // directory served under /static/ in -http mode
}

// The story surface stays addressable by the command's local names so
// page.go and the tests keep their shape.
type (
	Story     = storybook.Story
	StoryKind = storybook.StoryKind
	Page      = storybook.Page
)

const (
	KindPrint = storybook.KindPrint
	KindHTTP  = storybook.KindHTTP
)

// staticRoot is the absolute path of the directory served under /static/:
// -static when given, <pkgdir>/static otherwise.
func (b *Book) staticRoot() string {
	dir := b.StaticDir
	if dir == "" {
		dir = filepath.Join(b.Dir, "static")
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	return root
}

// staticHandler serves the application's real assets under /static/, so
// previews pick up stylesheet edits on a plain browser reload.
func (b *Book) staticHandler() http.Handler {
	root := b.staticRoot()
	if root == "" {
		return http.NotFoundHandler()
	}
	return http.StripPrefix("/static/", http.FileServer(http.Dir(root)))
}
