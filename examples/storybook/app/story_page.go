//go:build story

package app

import (
	"fmt"
	"html/template"
	"strings"
)

// Page stories: the primary kind. Each one calls a render function (view
// data -> HTML) with one data state, so a browser shows how the page's
// markup and CSS hold up without a store, a request, or real data.

var (
	alice   = &User{ID: "1", Name: "Alice", Tags: []string{"admin", "go"}, Badge: template.HTML(`<b class="badge">staff</b>`)}
	hostile = &User{ID: "2", Name: "<script>alert(1)</script>"}
	bob     = &User{ID: "3", Name: "Bob", Tags: []string{"go"}}
	long    = &User{
		ID:   "4",
		Name: "Maximiliana-Wolfeschlegelsteinhausenbergerdorff",
		Tags: []string{"admin", "go", "frontend", "on-call", "reviewer", "mentor", "release-manager", "i18n"},
	}
)

// StoryUser renders the user page for a typical user.
func StoryUser() {
	var b strings.Builder
	if err := RenderUser(&b, alice); err != nil {
		panic(err)
	}
	fmt.Print(b.String())
}

// StoryUserEscaped renders a hostile name and no tags.
func StoryUserEscaped() {
	var b strings.Builder
	if err := RenderUser(&b, hostile); err != nil {
		panic(err)
	}
	fmt.Print(b.String())
}

// StoryUserLongName renders a very long name and many tags.
func StoryUserLongName() {
	var b strings.Builder
	if err := RenderUser(&b, long); err != nil {
		panic(err)
	}
	fmt.Print(b.String())
}

// StoryUserList renders the list page with several users.
func StoryUserList() {
	var b strings.Builder
	if err := RenderUserList(&b, UserListView{Users: []*User{hostile, alice, bob}, Filters: []string{"sort=name"}}); err != nil {
		panic(err)
	}
	fmt.Print(b.String())
}

// StoryUserListEmpty renders the list page when nothing matches.
func StoryUserListEmpty() {
	var b strings.Builder
	if err := RenderUserList(&b, UserListView{Filters: []string{"sort=name", "tag=nobody"}}); err != nil {
		panic(err)
	}
	fmt.Print(b.String())
}
