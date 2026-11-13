# Process-bound host callbacks

Host builtins without `SyncCallbacks` receive a caller whose `Call`,
`CallCatching` and `Spawn` retain the process in which the host call ran.
Reusing the public VM for another root call does not retarget that caller.
After its captured process ends, callback entry returns process-exit (or a
finished process-exit task for `Spawn`) without executing script code.

This is the first, limited ownership change for
[#1062 B](https://github.com/podhmo/minigo/issues/1062). The public `VM.Call`
entry point still creates a fresh process when appropriate. Nested
`EnsureProc`/`ReleaseProc` scopes retain their existing behavior. A callback
between root calls is accepted while an explicit process scope is still held.
Host-driven `Spawn` before any root call keeps its detached-process behavior.

Same-owner synchronous callbacks reuse the active execution context. Foreign
or idle callbacks use a fresh child VM under the captured process. Existing
helper-goroutine detection preserves synchronous panic propagation through
blocking host calls; ordinary asynchronous panics still fail that process,
and FuncMap retains its catching policy. The existing spawn and call-boundary
death checks also apply when termination overlaps entry. This does not add a
new lifecycle state machine, join all host work on shutdown, or remove
goroutine-identity routing.

Reflected host-call arguments bind before conversion; function, interface/error,
and iterator adapters preserve an already-bound caller. FuncMap registration is
also an explicit retention boundary and binds its registered script functions.
Off-owner method probes and callback result conversion use private metadata
rather than the reusable root VM's frames/memos. This protects the
VM's execution state; it does not synchronize mutable script values captured
by a callback. Hosts still need their usual synchronization for those values.
Other `VMCaller` frame/type operations are execution-local, not a general
concurrent API. `SyncCallbacks` still promises never to retain the caller or
invoke it on another goroutine; violating that contract is unsupported.

Generic value/field adaptation from a raw VM is deliberately outside this
initial boundary. A package-global `sync.Pool.New`, for example, is a reusable
factory invoked by later root calls; automatically pinning it to the package's
initialization process would break engine reuse. Such field-stored factories
retain their existing behavior. Distinguishing reusable factories from retained
external entry points needs an explicit adapter contract before that boundary
can be migrated. This is a scoped first step, not complete process ownership
for every host value containing script callbacks.

`vm/processcaller_test.go` covers expired handles before/during VM reuse,
intentional new root entry, nested held scopes, same-owner reentry, private
idle-callback execution, reflected callbacks and reusable field factories.
The existing callback,
panic/recovery and template suites cover compatibility. Run
`make concurrency-stress` for the classified race matrix from #1063; passing
that bounded matrix is not a proof of all callback lifetimes or schedules.
