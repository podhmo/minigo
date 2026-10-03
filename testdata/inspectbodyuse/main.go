package main

import "minigo.dev/inspect"

func count(n *inspect.Node) int {
	total := 0
	if n.Kind == "CallExpr" {
		total++
	}
	for _, c := range inspect.SyntaxChildren(n) {
		total += count(c)
	}
	return total
}
func Walk() string {
	p := inspect.PackageOf("github.com/podhmo/minigo/experiments/httpinspect/testdata/handlers")
	d := inspect.Symbol(p, "Helper")
	n := inspect.Body(d)
	if n.Kind != "BlockStmt" || n.Role != "Body" || n.Owner.Name != "Helper" {
		return "bad root"
	}
	if count(n) != 3 {
		return "bad calls"
	}
	xs := inspect.SyntaxChildren(n)
	if xs[0].Role != "List" || xs[0].Index != 0 || xs[0].Token != ":=" || xs[0].Pos == "" {
		return "bad role"
	}
	if inspect.State(p) != "indexed" {
		return "executed target"
	}
	return "ok"
}
func FindType(n *inspect.Node) string {
	if n.Type != nil && n.Type.Text == "*web.Request" {
		sid := inspect.SymbolID(inspect.UnRef(n.Type))
		return sid.PackagePath + "." + sid.Name
	}
	for _, c := range inspect.SyntaxChildren(n) {
		s := FindType(c)
		if s != "" {
			return s
		}
	}
	return ""
}
func TypeContext() string {
	p := inspect.PackageOf("github.com/podhmo/minigo/experiments/httpinspect/testdata/handlers")
	return FindType(inspect.Body(inspect.Symbol(p, "InspectOnly")))
}
func HostTrap()        { p := inspect.PackageOf("strings"); inspect.Body(inspect.Symbol(p, "TrimSpace")) }
func NonFunctionTrap() { p := inspect.SourceOf("net/http"); inspect.Body(inspect.Symbol(p, "Request")) }
func ArityTrap()       { inspect.Body() }
