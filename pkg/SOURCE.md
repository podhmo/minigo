# Vendored sources

Code under `pkg/` was copied out of `github.com/podhmo/go-scan` so this
repository has no module dependency on go-scan.

## Source

- Repository: `https://github.com/podhmo/go-scan`
- Branch: `main`
- Commit: `87cffe179f571a1bc0d835f7e8ca8d2adfa51036`
- Retrieved: file copy (no git history carried over)

## What was copied

| Destination | Source | Notes |
| --- | --- | --- |
| `pkg/locator/` | `locator/` (locator.go, locator_test.go) | `scanner.Overlay` (`map[string][]byte`) replaced by the local `Overlay` type defined in `locator.go`; that was the only go-scan symbol the package used. External dep on `golang.org/x/mod` kept. |

To follow upstream changes, diff `pkg/locator/` against `locator/` in
go-scan at the commit above, then re-apply the `scanner.Overlay` →
`Overlay` substitution.
