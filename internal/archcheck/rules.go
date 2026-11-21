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
	".": {"builtin", "bytecode", "compile", "index", "inspect", "internal/assetaudit", "internal/bind", "internal/execution", "internal/hostbridge", "internal/replhooks", "internal/source", "internal/spike", "internal/stdlib", "internal/stdlib/context", "internal/stdlib/base64", "internal/stdlib/bufio", "internal/stdlib/bytes",
		"internal/stdlib/bytealg", "internal/stdlib/crand", "internal/stdlib/atomic", "internal/stdlib/csv", "internal/stdlib/filepath", "internal/stdlib/goruntime", "internal/stdlib/host", "internal/stdlib/os", "internal/stdlib/osexec", "internal/stdlib/errors", "internal/stdlib/godebug",
		"internal/stdlib/io", "internal/stdlib/importmanager", "internal/stdlib/maps", "internal/stdlib/reflect", "internal/stdlib/reflectlite", "internal/stdlib/unsafe", "internal/stdlib/inspect", "internal/stdlib/json", "internal/stdlib/fmt", "internal/typedef", "internal/vet",
		"internal/stdlib/slices", "internal/stdlib/sort",
		"internal/stdlib/hex", "internal/stdlib/math", "internal/stdlib/net", "internal/stdlib/netip", "internal/stdlib/path", "internal/stdlib/race",
		"internal/stdlib/regexp", "internal/stdlib/sha256", "internal/stdlib/strconv", "internal/stdlib/strings", "internal/stdlib/stringslite",
		"internal/stdlib/sync", "internal/stdlib/syscall", "internal/stdlib/testenv", "internal/stdlib/unicode", "internal/stdlib/html", "internal/stdlib/htmltemplate", "internal/stdlib/texttemplate", "internal/stdlib/time", "internal/stdlib/utf8", "minireflect", "repl", "resolve", "runtime", "special", "syntax", "vm"},
	"builtin":                       {"compile", "internal/hostbridge", "internal/source", "minireflect", "runtime", "vm"},
	"bytecode":                      {"syntax"},
	"cmd/minigo":                    {".", "index", "repl", "resolve", "runtime"},
	"compile":                       {"bytecode", "index", "internal/semantics", "internal/source", "internal/spike", "runtime", "syntax"},
	"host":                          {},
	"index":                         {"pkg/astwalk", "syntax"},
	"inspect":                       {"index", "pkg/astwalk", "resolve", "runtime", "syntax"},
	"internal/archcheck":            {"pkg/gentest"},
	"internal/assetaudit":           {"index", "syntax"},
	"internal/bind":                 {"runtime"},
	"internal/cmd/archcheck":        {"internal/archcheck"},
	"internal/cmd/genbind":          {".", "index"},
	"internal/cmd/genifaces":        {},
	"internal/cmd/simcheck":         {},
	"internal/execution":            {},
	"internal/hostbridge":           {"internal/process", "minireflect", "runtime"},
	"internal/process":              {},
	"internal/replhooks":            {"bytecode", "index", "internal/execution", "resolve", "runtime", "syntax", "vm"},
	"internal/semantics":            {"index", "syntax"},
	"internal/source":               {"index", "internal/semantics", "runtime", "syntax"},
	"internal/spike":                {},
	"internal/stdlib":               {"builtin", "internal/hostbridge", "runtime", "vm"},
	"internal/stdlib/context":       {"internal/hostbridge", "internal/stdlib", "runtime", "vm"},
	"internal/stdlib/atomic":        {"internal/stdlib", "runtime"},
	"internal/stdlib/goruntime":     {"internal/hostbridge", "internal/stdlib", "internal/stdlib/context", "runtime", "vm"},
	"internal/stdlib/errors":        {"internal/hostbridge", "internal/stdlib", "runtime"},
	"internal/stdlib/base64":        {"internal/stdlib", "internal/stdlib/io", "runtime"},
	"internal/stdlib/bufio":         {"internal/hostbridge", "internal/stdlib", "internal/stdlib/io", "runtime"},
	"internal/stdlib/bytes":         {"internal/stdlib", "runtime"},
	"internal/stdlib/csv":           {"internal/stdlib", "internal/stdlib/io", "runtime"},
	"internal/stdlib/filepath":      {"internal/bind", "internal/hostbridge", "internal/stdlib", "runtime"},
	"internal/stdlib/host":          {"internal/bind", "internal/stdlib", "runtime"},
	"internal/stdlib/importmanager": {"internal/stdlib", "pkg/importmanager", "runtime"},
	"internal/stdlib/os":            {"internal/bind", "internal/hostbridge", "internal/stdlib", "runtime", "vm"},
	"internal/stdlib/osexec":        {"internal/bind", "internal/stdlib", "internal/stdlib/context", "runtime"},
	"internal/stdlib/strings":       {"internal/hostbridge", "internal/stdlib", "runtime"},
	"internal/stdlib/crand":         {"internal/hostbridge", "internal/stdlib", "internal/stdlib/io", "runtime"},
	"internal/stdlib/net":           {"internal/stdlib", "runtime"},
	"internal/stdlib/hex":           {"internal/stdlib", "internal/stdlib/io", "runtime"},
	"internal/stdlib/bytealg":       {"internal/stdlib", "runtime"},
	"internal/stdlib/godebug":       {"internal/stdlib", "runtime"},
	"internal/stdlib/io":            {"internal/hostbridge", "internal/stdlib", "runtime"},
	"internal/stdlib/race":          {"internal/stdlib", "runtime"},
	"internal/stdlib/stringslite":   {"internal/stdlib", "runtime"},
	"internal/stdlib/testenv":       {"internal/stdlib", "runtime"},
	"internal/stdlib/netip":         {"internal/stdlib", "runtime"},
	"internal/stdlib/path":          {"internal/stdlib", "runtime"},
	"internal/stdlib/sha256":        {"internal/stdlib", "runtime"},
	"internal/stdlib/strconv":       {"internal/stdlib", "runtime"},
	"internal/stdlib/unicode":       {"internal/stdlib", "runtime"},
	"internal/stdlib/sync":          {"internal/stdlib", "internal/stdlib/context", "runtime"},
	"internal/stdlib/syscall":       {"internal/stdlib", "runtime"},
	"internal/stdlib/math":          {"internal/stdlib", "runtime"},
	"internal/stdlib/regexp":        {"internal/stdlib", "runtime"},
	"internal/stdlib/unsafe":        {"internal/hostbridge", "internal/stdlib", "minireflect", "runtime"},
	"internal/stdlib/reflect":       {"internal/hostbridge", "internal/stdlib", "minireflect", "runtime"},
	"internal/stdlib/reflectlite":   {"minireflect", "runtime"},
	"internal/stdlib/inspect":       {"index", "inspect", "internal/hostbridge", "internal/stdlib", "resolve", "runtime"},
	"internal/stdlib/json":          {"internal/hostbridge", "internal/stdlib", "internal/stdlib/io", "runtime"},
	"internal/stdlib/fmt":           {"internal/hostbridge", "internal/stdlib", "internal/stdlib/goruntime", "internal/stdlib/io", "internal/stdlib/json", "internal/stdlib/time", "minireflect", "runtime"},
	"internal/typedef":              {"bytecode", "index", "internal/hostbridge", "runtime", "syntax"},
	"internal/vet":                  {"index", "pkg/astwalk", "runtime", "syntax"},
	"internal/stdlib/maps":          {"internal/stdlib", "runtime"},
	"internal/stdlib/slices":        {"internal/hostbridge", "internal/stdlib", "minireflect", "runtime"},
	"internal/stdlib/sort":          {"internal/hostbridge", "internal/stdlib", "runtime", "vm"},
	"internal/stdlib/html":          {"internal/stdlib", "runtime"},
	"internal/stdlib/htmltemplate":  {"internal/stdlib", "runtime"},
	"internal/stdlib/texttemplate":  {"internal/stdlib", "runtime", "vm"},
	"internal/stdlib/time":          {"internal/hostbridge", "internal/stdlib", "runtime", "vm"},
	"internal/stdlib/utf8":          {"internal/hostbridge", "internal/stdlib", "runtime"},
	"minireflect":                   {"runtime", "syntax"},
	"pkg/astwalk":                   {},
	"pkg/importmanager":             {},
	"pkg/gentest":                   {},
	"pkg/locator":                   {},
	"pkg/modulewalk":                {"pkg/locator", "resolve", "syntax"},
	"repl":                          {".", "compile", "index", "internal/replhooks", "pkg/locator", "pkg/modulewalk", "resolve", "runtime", "syntax"},
	"resolve":                       {"pkg/locator"},
	"runtime":                       {"bytecode", "index", "internal/execution", "internal/spike", "special", "syntax"},
	"special":                       {"syntax"},
	"storybook":                     {".", "resolve"},
	"syntax":                        {},
	"tools/difffuzz":                {},
	"tools/tmpltests":               {},
	"vm":                            {"bytecode", "index", "internal/hostbridge", "internal/process", "internal/spike", "runtime", "syntax"},
}

// BannedCalls lists selector calls a package directory must never
// make — the ratchet for "queries go through the adapter" rules like
// M2-4's "compile never materializes an import itself". Keyed by
// package dir under the module root.
var BannedCalls = map[string][]string{
	"compile": {"Materialize", "EnsureReady", "EnsureReadyRun"},
	// the source adapter loads to Indexed only — a call that runs
	// package initialization would make a type query start user code
	"internal/source": {"EnsureReady", "EnsureReadyRun", "EnsureConstsRun", "EnsureCompiled"},
}

// BannedImports lists import paths a package directory must never
// import — the P2-7 ratchet that all reflect-driven host-value work
// lives behind internal/hostbridge so vm/ stays reflect-free. Keyed by
// package dir under the module root.
var BannedImports = map[string][]string{
	"vm": {"reflect"},
}

// SharedStateTypes are the execution-state types that must never appear
// in shared assets or their cache entries (M1-3): script cells,
// functions, type definitions and import refs are per-session runtime
// state. Keyed by fully qualified "pkg/path.Type".
var SharedStateTypes = map[string]bool{
	"github.com/podhmo/minigo/runtime.Cell":      true,
	"github.com/podhmo/minigo/runtime.Function":  true,
	"github.com/podhmo/minigo/runtime.TypeDef":   true,
	"github.com/podhmo/minigo/runtime.ImportRef": true,
}

// SharedFuncExempt lists fields allowed to hold forbidden or
// function-typed data inside shared assets: "StructName.FieldName" for
// struct fields, or the bare type name for a named func type. These are
// host-provided callbacks wired at engine construction, or known debt
// documented here until it is redesigned away.
var SharedFuncExempt = map[string]bool{
	"sharedAssets.hostPolicy": true, // host config: decides which bound symbols are visible
	"SpecialFunc":             true, // host-registered special forms (engine.Bind specials table)
	// Known debt: the script profiler keeps *runtime.Function only as
	// map keys for naming sampled frames (vm/profile.go). It is off by
	// default; revisit when the profiler moves out of shared assets.
	"profFrame.fn":   true,
	"Profiler.fnIDs": true,
}
