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
	".":                      {"bytecode", "compile", "index", "inspect", "internal/assetaudit", "internal/replhooks", "internal/spike", "minireflect", "repl", "resolve", "runtime", "syntax", "vm"},
	"bytecode":               {"syntax"},
	"cmd/minigo":             {".", "index", "repl", "resolve", "runtime"},
	"compile":                {"bytecode", "index", "internal/semantics", "internal/source", "internal/spike", "runtime", "syntax"},
	"host":                   {},
	"index":                  {"syntax"},
	"inspect":                {"index", "resolve", "runtime", "syntax"},
	"internal/archcheck":     {"pkg/gentest"}, // test-only dep
	"internal/cmd/archcheck": {"internal/archcheck"},
	"internal/assetaudit":    {"index", "syntax"},
	"internal/replhooks":     {"bytecode", "index", "resolve", "runtime", "syntax", "vm"},
	"internal/semantics":     {"index", "syntax"},
	"internal/source":        {"index", "internal/semantics", "runtime", "syntax"},
	"internal/spike":         {},
	"minireflect":            {"runtime", "syntax"},
	"pkg/gentest":            {},
	"pkg/locator":            {},
	"repl":                   {".", "compile", "index", "internal/replhooks", "resolve", "runtime", "syntax"},
	"resolve":                {"pkg/locator"},
	"runtime":                {"bytecode", "index", "internal/spike", "syntax"},
	"storybook":              {".", "resolve"},
	"syntax":                 {},
	"tools/difffuzz":         {},
	"tools/tmpltests":        {},
	"vm":                     {"bytecode", "index", "internal/spike", "minireflect", "runtime", "syntax"},
}

// BannedCalls lists selector calls a package directory must never
// make — the ratchet for "queries go through the adapter" rules like
// M2-4's "compile never materializes an import itself". Keyed by
// package dir under the module root.
var BannedCalls = map[string][]string{
	"compile": {"Materialize", "EnsureReady", "EnsureReadyRun"},
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

// FileBudgets is the M1-4 file-size ratchet: recorded line counts for
// every non-test, non-generated .go file. A file may only shrink — when
// it does, lower the number; a grow or an unlisted new file fails CI.
// Regenerate with `go run ./internal/cmd/archcheck -print-sizes`.
var FileBudgets = map[string]int{
	"asmimpl.go":                         279,
	"builtins.go":                        1223,
	"bytecode/bytecode.go":               299,
	"bytecode/disasm.go":                 277,
	"cmd/minigo/main.go":                 1231,
	"compile/assign.go":                  880,
	"compile/call.go":                    810,
	"compile/compile.go":                 408,
	"compile/constfold.go":               861,
	"compile/expr.go":                    687,
	"compile/goto.go":                    103,
	"compile/lit.go":                     661,
	"compile/scope.go":                   536,
	"compile/special.go":                 53,
	"compile/stmt.go":                    1077,
	"compile/typehint.go":                1975,
	"dispatch.go":                        1151,
	"embed.go":                           271,
	"host/host.go":                       42,
	"hostslice.go":                       448,
	"hostvars.go":                        54,
	"index/index.go":                     311,
	"index/value_specs.go":               45,
	"inspect.go":                         1192,
	"inspect/inspect.go":                 2543,
	"inspect/stub.go":                    382,
	"internal/archcheck/archcheck.go":    536,
	"internal/archcheck/rules.go":        228,
	"internal/assetaudit/assetaudit.go":  112,
	"internal/cmd/archcheck/main.go":     96,
	"internal/replhooks/replhooks.go":    68,
	"internal/semantics/semantics.go":    149,
	"internal/source/source.go":          105,
	"internal/spike/spike_off.go":        45,
	"internal/spike/spike_on.go":         49,
	"intrinsics.go":                      1607,
	"intrinsics_bufio.go":                106,
	"intrinsics_bytes.go":                109,
	"intrinsics_context.go":              279,
	"intrinsics_crypto_rand.go":          97,
	"intrinsics_crypto_sha256.go":        33,
	"intrinsics_encoding_base64.go":      17,
	"intrinsics_encoding_csv.go":         29,
	"intrinsics_encoding_hex.go":         17,
	"intrinsics_encoding_json.go":        2312,
	"intrinsics_errors.go":               660,
	"intrinsics_fmt.go":                  1786,
	"intrinsics_host.go":                 38,
	"intrinsics_html.go":                 15,
	"intrinsics_html_template.go":        50,
	"intrinsics_internal_bytealg.go":     48,
	"intrinsics_internal_godebug.go":     60,
	"intrinsics_internal_race.go":        21,
	"intrinsics_internal_stringslite.go": 33,
	"intrinsics_internal_testenv.go":     23,
	"intrinsics_io.go":                   417,
	"intrinsics_maps.go":                 245,
	"intrinsics_math.go":                 117,
	"intrinsics_net.go":                  172,
	"intrinsics_net_netip.go":            39,
	"intrinsics_os.go":                   465,
	"intrinsics_os_exec.go":              63,
	"intrinsics_path.go":                 25,
	"intrinsics_path_filepath.go":        131,
	"intrinsics_regexp.go":               21,
	"intrinsics_runtime.go":              274,
	"intrinsics_slices.go":               626,
	"intrinsics_sort.go":                 229,
	"intrinsics_strconv.go":              89,
	"intrinsics_strings.go":              163,
	"intrinsics_sync.go":                 54,
	"intrinsics_sync_atomic.go":          299,
	"intrinsics_syscall.go":              29,
	"intrinsics_text_template.go":        82,
	"intrinsics_time.go":                 486,
	"intrinsics_unicode.go":              66,
	"intrinsics_unicode_utf8.go":         137,
	"intrinsics_unsafe.go":               367,
	"iterseq.go":                         915,
	"minigo.go":                          1542,
	"minigo_reflect.go":                  48,
	"minireflect/minireflect.go":         1183,
	"minireflect/rtype.go":               1861,
	"minireflect/rvalue.go":              3692,
	"native.go":                          258,
	"parse.go":                           127,
	"parsecache.go":                      260,
	"pkg/gentest/gentest.go":             227,
	"pkg/gentest/writer.go":              72,
	"pkg/locator/locator.go":             747,
	"repl/complete.go":                   1389,
	"repl/load.go":                       658,
	"repl/repl.go":                       1257,
	"replhooks.go":                       53,
	"resolve/filescope.go":               117,
	"resolve/goscan.go":                  123,
	"resolve/lang.go":                    87,
	"resolve/repo.go":                    201,
	"resolve/resolve.go":                 289,
	"runtime/arraylen.go":                137,
	"runtime/errors.go":                  111,
	"runtime/hostseq.go":                 130,
	"runtime/package.go":                 750,
	"runtime/typedef.go":                 1705,
	"runtime/unwrap.go":                  106,
	"runtime/value.go":                   3384,
	"runtime/walk.go":                    131,
	"storybook/storybook.go":             331,
	"storybook/watch.go":                 324,
	"syntax/complit.go":                  30,
	"syntax/lang.go":                     487,
	"syntax/scope.go":                    129,
	"syntax/syntax.go":                   193,
	"syntax/universe.go":                 29,
	"tools/difffuzz/gen.go":              542,
	"tools/difffuzz/lang.go":             314,
	"tools/difffuzz/main.go":             605,
	"tools/difffuzz/probe.go":            284,
	"tools/difffuzz/reflect.go":          671,
	"tools/difffuzz/run.go":              258,
	"tools/difffuzz/text.go":             205,
	"tools/tmpltests/main.go":            816,
	"tools/tmpltests/shims.go":           130,
	"vet.go":                             239,
	"vm/assert.go":                       1732,
	"vm/binop.go":                        1637,
	"vm/call.go":                         736,
	"vm/composite.go":                    3151,
	"vm/const.go":                        1283,
	"vm/convert.go":                      1416,
	"vm/ffi_iface.go":                    591,
	"vm/ffi_marshal.go":                  2840,
	"vm/frame.go":                        200,
	"vm/generic.go":                      1970,
	"vm/goroutine_id.go":                 36,
	"vm/goroutine_id_asm.go":             17,
	"vm/hostslice.go":                    231,
	"vm/iter.go":                         336,
	"vm/loop.go":                         999,
	"vm/panic.go":                        542,
	"vm/processcaller.go":                97,
	"vm/profile.go":                      336,
	"vm/sched.go":                        694,
	"vm/select.go":                       154,
	"vm/special.go":                      280,
	"vm/trace.go":                        216,
	"vm/vm.go":                           883,
}
