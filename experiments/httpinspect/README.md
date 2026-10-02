# Function-body inspection experiment

This bounded abstract interpreter consumes `inspect.BodyOf` / `Node.ChildNodes`
views and collects OpenAPI parameter **candidates** from a source HTTP handler.
It does not execute the target, initialize imports, generate an OpenAPI document,
or infer a validated request contract. The Japanese plan and findings are in
[the experiment report](../../docs/ja/sketch/experiment-inspect-bodies.md).

From the repository root:

```sh
go run ./experiments/httpinspect/cmd/httpinspect \
  github.com/podhmo/minigo/experiments/httpinspect/testdata/handlers Helper
go test ./experiments/httpinspect ./inspect ./ -run 'Test(Analyze|Budget|InspectBodyScript|BodyShapes|LazyBoundaries|ConcreteExecutionBaseline)$'
```

The CLI also accepts a source import path and a top-level function name. The
entry needs an explicitly declared `*net/http.Request` parameter. Request aliases,
framework request wrappers, methods, closures, and indirect function values need
further binding work. The fixtures intentionally panic in `init`; successful
analysis therefore demonstrates that those packages were never initialized.

Supported transfer rules include assignments, local value declarations, blocks,
if/else alternatives, positional and named returns, direct source helper calls,
and four request/conversion summaries: `URL.Query`, query/header `Get`,
`Request.PathValue`, and `strconv.Atoi`. The call trace records abstract arguments,
results, and summary boundaries. Recursion and branching consume depth/step
budgets (defaults: 8 / 2000). Traces are appended when calls finish.

`schema.type = integer` means an observed `Atoi` conversion, not proof that all
accepted inputs are integers. Query/header requiredness is left false; path
candidates use true. Branch reachability, error validation, response schemas,
route registration, and defaults are outside this prototype. A diagnostic makes
`incomplete` true; no diagnostics means only that the traversed syntax fits this
prototype's supported rules. Unsupported statements invalidate local abstract
values, and opaque call results become unknown. Unknown names are never invented
as concrete parameters. Globals, heap effects, full expression typing, and method
dispatch are not modeled. Import names inherit inspect's basename approximation
for unaliased imports; explicit aliases are the reliable path in this experiment.

The reusable inspect layer provides syntax, source context, and explicit type
positions. All HTTP semantics, call policy, abstract values, and budgets belong
to this experiment, independently of the normal compiler and VM.
