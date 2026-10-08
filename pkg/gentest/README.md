# gentest

Test harness for the generator examples (`examples/gen-sync`,
`examples/convert-define`): build a temporary module, run the tool
against it, assert on every file the run produced — instead of
hand-rolled temp dirs and per-file comparisons in each example.

Design adapted from go-scan's `scantest` + `writer.go` (see
`pkg/SOURCE.md` for the source commit and the porting notes).

## The loop

```go
dir := gentest.WriteFiles(t, map[string]string{
    "go.mod": "module example.com/m\n\ngo 1.26\n",
    "main.go": fixtureSource,
})
gentest.CopyTree(t, "app", filepath.Join(dir, "app")) // fixture packages

res, err := gentest.Run(t, ctx, dir, func(ctx context.Context) error {
    return runTheTool(ctx, dir) // engine.Run, a host run(), ...
})
if err != nil { t.Fatal(err) }

// the complete write set — unexpected writes can't hide:
if diff := cmp.Diff(wantPaths, res.ChangedPaths()); diff != "" { ... }
// res.Outputs (created) / res.Modified (changed) / res.Deleted

gentest.AssertSameFile(t, filepath.Join(dir, "app", "job.go"), "testdata/job.golden")
```

`Run` diffs the directory before and after the action. That filesystem
diff is the primary source of truth: generator writes that cross the
interpreter boundary (a script calling `os.WriteFile` inside minigo)
cannot be intercepted, and the snapshot sees them anyway.

## FileWriter — in-memory capture for host-side writes

Host-side generator code routes output writes through
`gentest.WriteFile`, which dispatches to a `FileWriter` installed on the
context — `os.WriteFile` when none is installed (production unchanged).

```go
// tool code:
return gentest.WriteFile(ctx, output, formatted, 0644)

// test code:
mem := &gentest.MemoryFileWriter{BaseDir: dir}
ctx := gentest.WithFileWriter(ctx, mem)
run(ctx, ...)
// mem.Outputs["generated.go"] holds the bytes; nothing hit disk.
```

`FileWriterFunc` adapts a function for failure injection (refuse one
path without chmod games).

## Live usage examples

- `examples/gen-sync/main_test.go` — `WriteFiles`/`CopyTree` fixture
  module + `Run` write-set assertions for the interpreted script's
  `os.WriteFile` outputs (the fs-diff pattern).
- `examples/convert-define/integration_test.go` — `gentest.WriteFiles`
  for the temp module and `MemoryFileWriter` capture of the
  host-written `generated.go` (the FileWriter pattern).
- `gentest_test.go` — a minimal self-test walking both patterns.
