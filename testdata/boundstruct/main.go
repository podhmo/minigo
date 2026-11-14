package main

import (
	"html/template"
	"strings"

	"example.com/store"
)

var tmpl = template.Must(template.New("t").Parse(`{{.ID}}:{{.Title}}:{{.Done}}`))

// Render executes the bound html/template on a real host struct a
// composite literal of the bound type minted.
func Render() string {
	var sb strings.Builder
	iss := store.Issue{ID: 7, Title: "x"}
	if err := tmpl.Execute(&sb, iss); err != nil {
		return "error: " + err.Error()
	}
	return sb.String()
}

// RenderZero renders the declared zero: `var iss store.Issue` mints the
// host type's zero through the same HostNew path.
func RenderZero() string {
	var sb strings.Builder
	var iss store.Issue
	if err := tmpl.Execute(&sb, iss); err != nil {
		return "error: " + err.Error()
	}
	return sb.String()
}

// Fields reads and writes the literal's fields as the host struct's —
// assignments land on the box the literal produced.
func Fields() string {
	iss := store.Issue{ID: 7, Title: "x"}
	iss.Done = true
	iss.Title = "y"
	return tmplRender(iss)
}

func tmplRender(iss store.Issue) string {
	var sb strings.Builder
	if err := tmpl.Execute(&sb, iss); err != nil {
		return "error: " + err.Error()
	}
	return sb.String()
}

// RoundTrip hands a literal-built host struct back to the bound
// package's own function.
func RoundTrip() string {
	iss := store.Issue{ID: 7, Title: "x", Done: true}
	return store.Describe(iss)
}

// BadField traps on a field the host struct does not declare — the
// literal's "unknown field" is a compile-time error in gc, so the VM
// trap is not a script-recoverable panic.
func BadField() {
	_ = store.Issue{Nope: 1}
}

func main() {}

// InSlice passes literal-minted host structs as a []store.Issue —
// each element boxes *T, the by-value slice param wants T elements.
func InSlice() int64 {
	return store.SumIDs([]store.Issue{store.Issue{ID: 1}, store.Issue{ID: 2}})
}

// InMap passes the same through a map[string]store.Issue.
func InMap() int64 {
	return store.SumMap(map[string]store.Issue{"a": store.Issue{ID: 3}})
}

// InSliceElided uses the elided-element form []T{{...}} — each elided
// literal is still a bound-type composite.
func InSliceElided() int64 {
	return store.SumIDs([]store.Issue{{ID: 4}})
}
