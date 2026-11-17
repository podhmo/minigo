package archcheck

// Rules is the allow list of module-internal import edges, keyed by the
// importing package's path relative to the module root ("." is the root
// package). It starts as a pin of the current graph (M1-2) and tightens
// per docs/next/ja/references/archcheck.md. Every allowed edge must
// exist in the code and every existing edge must be allowed — the check
// fails on stale entries too, so the table always mirrors reality.
// Regenerate with `go run ./internal/cmd/archcheck -print`.
//
// A pkg_test file importing its own package does not count as an edge,
// so self-edges never appear.
var Rules = map[string][]string{
	".":                      {"bytecode", "compile", "index", "inspect", "internal/replhooks", "internal/spike", "minireflect", "repl", "resolve", "runtime", "syntax", "vm"},
	"bytecode":               {"syntax"},
	"cmd/minigo":             {"index", "repl", "resolve", "runtime"},
	"compile":                {"bytecode", "index", "internal/spike", "runtime", "syntax"},
	"host":                   {},
	"index":                  {"syntax"},
	"inspect":                {"index", "resolve", "runtime", "syntax"},
	"internal/archcheck":     {"pkg/gentest"}, // test-only dep
	"internal/cmd/archcheck": {"internal/archcheck"},
	"internal/replhooks":     {"bytecode", "index", "resolve", "runtime", "syntax", "vm"},
	"internal/spike":         {},
	"minireflect":            {"runtime"},
	"pkg/gentest":            {},
	"pkg/locator":            {},
	"repl":                   {"compile", "index", "internal/replhooks", "resolve", "runtime", "syntax"},
	"resolve":                {"pkg/locator"},
	"runtime":                {"bytecode", "index", "internal/spike", "syntax"},
	"storybook":              {"resolve"},
	"syntax":                 {},
	"tools/difffuzz":         {},
	"tools/tmpltests":        {},
	"vm":                     {"bytecode", "index", "internal/spike", "minireflect", "runtime", "syntax"},
}
