# Go doc policy and package review

The documentation is shorter after removing repeated API inventories
and implementation explanations, while preserving calling contracts. Changes
cover 31 packages, each in its own commit. The policy now explicitly favors
short maps and meaningful contracts over exhaustive cross-links and uniform
comment coverage.

## Review scope

- Date: 2026-10-19, Asia/Tokyo.
- Base: fetched `origin/docs/godoc-policy`, commit `88d3325d`.
- Branch: `codex/godoc-slim-review`.
- Worktree: `/private/tmp/minigo-godoc-review`.
- Package inventory: 49 directories with non-test Go sources outside `testdata`,
  including all six example modules and the two tools directories. No `go list`
  was used. Every directory was visited with both `go doc .` and `go doc -all .`
  from its own directory, before and after editing.
- Rendering succeeded for 48 directories. The tagged convert-define e2e fixture
  is excluded by the current build constraints; its output is not certified.
  `testdata`, test-only packages and alternate build configurations are outside
  this rendered review.

All changes to existing Go files preserve their non-comment token streams and
Go directives. Three new doc.go files contain only package documentation and a
package clause. No API signatures or executable statements changed.

## Findings and policy changes

The original policy had the right audience but also created pressure to expand
comments. It asked for a fixed overview structure, links to every needed exported
identifier, and origin/next-operation explanations on every type. Together with
an exception for tautologies to make sibling lists look uniformly documented,
those rules encouraged a second API index in prose.

The revised policy removes that pressure. A package map should identify an entry
point and a useful flow; generated listings provide the full inventory. Examples
and type-provenance explanations are conditional on helping the caller. The
repeated sentence checklists are consolidated into the contract rules and a
rendered verification section.

Two real concatenation problems were visible in go doc: cmd/minigo included
REPL history and line-editing internals, and gen-sync/scanx included descriptions
of explore.go and inspect.go. Their headers are now detached. Locator lacked a
package map, as did several substantive convert-define helper packages; those
now have short starting routes.

The extract-schema examples contained stale API descriptions: plugin discovery
uses exported `func(*extschema.Config)` functions regardless of name, rather than
requiring `Overrides()`, `Config()` or `Configure`. The relevant Go comments now
match that implementation. The CLI flag help still says `Overrides()`; that
separate string correction is tracked in TODO.md.

Low-level technical terms are not automatically implementation leakage. Bytecode
operands, VM lifecycle, representation rules, hook requirements and cache
invalidation matter to consumers of those packages. Their necessary detail is
retained. Familiar reflect methods also remain undocumented where the shared API
and named deviations supply enough context.

Fixture comments can be generator inputs. For example, derivingconvert annotations
and schema-description comments are part of the examples' behavior. The policy
now distinguishes these from caller libraries so a cosmetic compliance pass does
not rewrite input data or add useless navigation paragraphs.

## Package decisions

“Retained” means the visible output was reviewed and left unchanged; it does not
mean a new filler commit was created. Fixture rows record intentional preservation
under the clarified scope. Paths are repository-relative.

| Package directory | Decision | Commit |
| --- | --- | --- |
| `.` | Shorter entry map; retain run/load/call, host binding and result conversion routes. | `bfbacb3d` |
| `bytecode` | Retained: operands, stack effects and shared resolution-cache rules are contracts for bytecode consumers. | — |
| `cmd/minigo` | Detach history and line-editor headers; put the sole command overview in doc.go. | `e78cd286` |
| `compile` | Remove opcode and name-lookup mechanics from the overview; retain delayed unsupported-feature failures. | `ab1e8ff8` |
| `examples/convert-define` | Add missing command invocation, output controls and rule-package route. | `740a1c77` |
| `examples/convert-define/convutil` | Add a small map to the two time-conversion rules. | `d49de242` |
| `examples/convert-define/define` | Remove signature repetition and repeated examples; keep literal/identifier syntax and pointer-path behavior. | `a0f3dc20` |
| `examples/convert-define/e2e_test` | Excluded by build constraints in go doc; contains tagged generated code and definition input, not a caller library. | — |
| `examples/convert-define/generator` | Add Generate/Options entry map; shorten ImportManager.Imports without losing copy/exclusion semantics. | `78fae6ff` |
| `examples/convert-define/internal` | Replace interpreter architecture with NewRunner → Run → Info/TypeResolver; retain positioned DSL errors. | `117a077c` |
| `examples/convert-define/model` | Add the rules, struct lookup and error-collection map; keep type-resolution contracts. | `782f3e38` |
| `examples/convert-define/sampledata/destination` | Fixture: preserve example type shapes and comments. | — |
| `examples/convert-define/sampledata/funcs` | Fixture helpers: preserve rule inputs and sample conversions. | — |
| `examples/convert-define/sampledata/source` | Inspection input: preserve derivingconvert annotations and computed-field directives. | — |
| `examples/extract-schema` | Correct the package-doc plugin flag description to signature-based hooks. | `69383412` |
| `examples/extract-schema/app` | Inspection fixture: preserve doc text used for schema descriptions and comment-filter coverage. | — |
| `examples/extract-schema/app/internal/dep` | Cross-package schema fixture: preserve input descriptions. | — |
| `examples/extract-schema/extschema` | Replace the comparison to another example with Config and registration methods; remove future-validation rationale. | `aa598622` |
| `examples/extract-schema/plugins/desc` | Remove obsolete Config()/map-key explanation; document Configure and the any/reflect boundary. | `e5118b9c` |
| `examples/extract-schema/plugins/enumcode` | Retained: small example explains the schema override behavior; Install is visible in the generated API listing. | — |
| `examples/extract-schema/plugins/time` | Replace the nonexistent required Configure name with Install and signature discovery. | `caabf739` |
| `examples/extract-schema/script` | Add Main route; remove nonexistent overrides parameter; document pluginDirs hooks and mode arguments. | `e38182f3` |
| `examples/gen-sync` | Retained: concise command invocation and flag map. | — |
| `examples/gen-sync/app` | Generation fixture: preserve explanatory comments and existing managed directives. | — |
| `examples/gen-sync/app/internal/bound` | Fixture for bound-package source lookup; preserve its explanation. | — |
| `examples/gen-sync/app/internal/envel` | Fixture with package name shade; preserve directory/name mismatch explanation. | — |
| `examples/gen-sync/app/internal/meta` | Dependency fixture; preserve comments distinguishing non-target declarations. | — |
| `examples/gen-sync/app/internal/mood` | Dependency-scope fixture; preserve its generation inputs. | — |
| `examples/gen-sync/scanx` | Detach two internal file headers; add Explorer and directive-region flow; shorten traversal and method-filter docs. | `be858d65` |
| `examples/gen-sync/script` | Replace file-organization prose with Main and the scanx route; keep inference semantics. | `597cdbd8` |
| `examples/minigo-generate` | Retained: shared-engine behavior and invocation affect command users. | — |
| `examples/minigo-generate/app` | Generation fixture: preserve directives and enum test inputs. | — |
| `examples/minigo-generate/tools/enumvals` | Replace demo/history prose with invocation and Main. | `b1420c1b` |
| `examples/minigo-generate/tools/stringer` | Remove issue number and internal-index rationale; retain invocation and directive environment. | `4240e4cb` |
| `examples/task-run` | Retained: invocation, dry-run effects and task signatures are caller contracts. | — |
| `examples/task-run/task` | Replace stub/tooling rationale with Run/Sh/Deps/F and interpreter-only usage; retain task shapes. | `8602a0fd` |
| `examples/test-detect` | Keep invocation and repository/network limits; remove parser and reverse-graph implementation detail. | `36ef00ff` |
| `host` | Shorten the import/entry map; retain interpreter-only use, real process state and restricted-engine behavior. | `ebb65067` |
| `index` | Remove the runtime storage narrative; retain Build/BuildLenient distinction and AST helper routes. | `d1d71c0c` |
| `inspect` | Replace the API inventory and long example with a short script/host map; retain traps, lookup misses and bound-source rules. | `c8fe8a39` |
| `minireflect` | Remove duplicated domain/interning overview; keep those contracts on RType/RValue and retain unsupported operations. | `66d28b1d` |
| `pkg/gentest` | Replace the long inline test with the fixture → Run → assertions flow; retain memory-write exclusion. | `b0ebbf7b` |
| `pkg/locator` | Add missing package map; replace provenance in Overlay docs with the actual go.mod key contract; retain provenance as source comment. | `c4eee4b9` |
| `resolve` | Keep resolver setup, file-selection and miss-error routes; remove the secondary API inventory. | `182ecf4b` |
| `runtime` | Shorten the value-map inventory; reduce virtual-slice field rationale and state the VMCaller.Convert panic boundary. | `a1560f8e` |
| `syntax` | Keep ParseFile/File and CheckLang/DeclaredKinds; remove obvious helper enumeration. | `212d15b7` |
| `tools/difffuzz` | Retained: generation constraints, verdicts, shrinking and process outcomes define harness behavior. | — |
| `tools/tmpltests` | Replace scratch-GOROOT architecture with invocation, selection and retained-scratch guidance. | `ce285ea5` |
| `vm` | Remove bare interface-implementation one-liners; retain lifecycle, callback, nil-hook and error contracts. | `9468b83e` |

## Policy follow up

Following review feedback, prose-link reachability is no longer a requirement,
including for representative journeys. Sibling comparisons now explain the
choice-changing difference, using inspect's bound/source loading, Lookup misses,
declared/promoted methods and script/host error behavior as examples. The inspect
overview replaces a navigation inventory with these distinctions.

## Size and preserved contracts

The counts below describe the initial 31-package review before this follow-up.

Whitespace-separated word counts in complete `go doc -all` output, including
signatures and fields, changed from 37,515 to 35,739 across the 48 rendered
directories: 1,776 fewer words, or 4.7%. The policy changed from 1,050 to 665
words, a 36.7% reduction. Counts are a scale indicator, not a compliance test;
missing maps were added where useful.

| Selected rendered package | Before | After |
| --- | ---: | ---: |
| minigo | 2,792 | 2,689 |
| cmd/minigo | 126 | 57 |
| inspect | 4,747 | 4,521 |
| runtime | 10,421 | 10,204 |
| minireflect | 1,808 | 1,707 |
| pkg/gentest | 670 | 573 |
| examples/gen-sync/scanx | 1,039 | 809 |
| examples/convert-define/define | 456 | 316 |

Retained contracts include Engine.Call argument copying and goroutine teardown,
load/cache/initialization distinctions, REPL persistence and := behavior, lookup
nil-versus-trap distinctions, type-expression navigation, map NaN behavior,
shared-container conversion, virtual slices, method-set rules and VM process
lifetimes. The package summaries point to the identifiers that own these details.

The largest remaining rendered output is runtime's exposed representation and
helper surface. Reducing that substantially further would require decisions about
API visibility or moving contracts out of go doc, rather than deleting duplicated
prose. This review preserves that public surface.

## Validation

- `make format`: passed using the repository's goimports target.
- `make lint`: passed, including go vet and staticcheck.
- `make test`: passed for the root module and all six example modules.
- `git diff --check`: passed.
- Go scanner comparison: all 35 modified existing Go files have identical
  non-comment token streams to the base; source directives are preserved.
- Both go doc modes were reread after editing. Remaining bracket candidates
  were checked as syntax, generic arguments, indexing or command arguments;
  no unresolved prose doc links were identified in the rendered output.
- Direct navigation checks included Engine.Run, inspect.Decl, locator.Overlay,
  runtime.VMCaller.Convert and scanx.Explorer.Reach.

The first sandboxed test attempt could not bind its loopback HTTP listener;
the complete test target passed with that restriction lifted. Go and staticcheck
caches were placed under /private/tmp without changing GOPATH. These environment
failures were not interpreted as code defects.

## Commit organization

Policy clarification is commit `a5b5dde5`. The package table identifies
all 31 package commits. Follow-up tracking is commit `aaa3e053`; the
report is saved on the same branch. Packages without changes have no artificial
empty commits. The branch is published in [PR 912](https://github.com/podhmo/minigo/pull/912);
the policy follow-up and inspect comparison changes are additional commits.
