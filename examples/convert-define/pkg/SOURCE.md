# Vendored sources

Code under `pkg/` was copied out of `github.com/podhmo/go-scan` so this
module has no module dependency on go-scan.

## Source

- Repository: `https://github.com/podhmo/go-scan`
- Branch: `main`
- Commit: `87cffe179f571a1bc0d835f7e8ca8d2adfa51036`
- Retrieved: file copy (no git history carried over)

## What was copied

Only the API surface `convert-define` actually calls. The vendored code was
slimmed down after vendoring: the symbol cache (`cache.go`), module walker,
type-relation walker, writer, overlays, constant evaluation, enum analysis,
inspect/log plumbing and every vendored test were dropped, so what remains
here is intentionally much smaller than upstream.

| Destination | Source | Notes |
| --- | --- | --- |
| `pkg/goscan/` | `goscan.go` and `importmanager.go` at the repository root | `package goscan`; trimmed to `New`, the `With*` options used here, `ScanPackageFromImportPath`, `ResolveType` and `ImportManager` |
| `pkg/scanner/` | `scanner/` (`models.go`, `scanner.go`) | trimmed to the type/struct/function declarations `convert-define` reads; `Overlay`, `ConstantInfo`, `VariableInfo` etc. removed |
| `pkg/locator/` | `locator/` (`locator.go`) | `ResolvePkgPath` and overlay support removed |

The libraries copied from `examples/convert/` upstream live at the module
root, not under `pkg/`: `model/`, `generator/`, `convutil/`,
`sampledata/{source,destination,funcs}`.

Not copied (not referenced by this module): `symgo/`, `minigo/`, `minigo2/`,
`tools/`, `docs/`, `examples/convert/{parser,mapping,main.go,testdata,generated_test}`,
`examples/convert/sampledata/{converter,generated,tags}`, `scantest/`,
`testdata/` — and every upstream `*_test.go`.

## Rewrites applied

- `github.com/podhmo/go-scan` (root package) → `github.com/podhmo/minigo/examples/convert-define/pkg/goscan`
- `github.com/podhmo/go-scan/<pkg>` → `github.com/podhmo/minigo/examples/convert-define/pkg/<pkg>`
- `github.com/podhmo/go-scan/examples/convert/` → `github.com/podhmo/minigo/examples/convert-define/`
- Log label `"component": "go-scan"` was left as upstream wrote it.
