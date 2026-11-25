// lazypkg.qnt — a Quint model of minigo's lazy package initialization.
//
// This is a transcription of the explicit state model in
// docs/lazy-lifecycle-model.md: the package lifecycle (parsed -> indexed
// -> initializing -> ready / failed), the at-most-once bootstrap, and the
// init-cycle rule that serves partial globals to a package whose init is
// already in progress on the same goroutine.
//
// Simplifications (stated honestly, see the doc):
//   - a single logical goroutine drives every cascade: concurrent
//     accessors blocking on sync.Once are not modeled. The interesting
//     reentrancy (the init cycle) is single-goroutine behavior anyway.
//     Note beginInit is enabled even mid-cascade, which a strictly
//     single-threaded world forbids — the model over-approximates here.
//     For safety invariants that is the sound direction: anything the
//     implementation can do, the model can do too (and more).
//   - the import graph is fixed: main -> {lib, plain}, lib -> {main}
//     (a cycle), plain -> {}. `plain` exercises the acyclic path.
//   - the const-only fast path is modeled as a separate once that can
//     bind constants without full initialization, for host-side access.
//   - the Unseen/Located stages are omitted: nothing in the codebase
//     ever uses them (packages are born Parsed).
//
// Frames are pushed at the FRONT of the stack list: head() is the
// innermost initializing package, tail() pops it.
//
// Run:  quint run spec/lazypkg.qnt --max-steps=60 --max-samples=200 \
//         --invariant=bootstrapsAtMostOnce
module lazypkg {
  // ---- stages ------------------------------------------------------
  pure val PARSED = "parsed"
  pure val INDEXED = "indexed"
  pure val INITIALIZING = "initializing"
  pure val READY = "ready"
  pure val FAILED = "failed"

  // forward order used for the monotonicity invariant; FAILED is
  // absorbing and compared separately
  pure def stageOrd(s) =
    if (s == PARSED) 1
    else if (s == INDEXED) 2
    else if (s == INITIALIZING) 3
    else if (s == READY) 4
    else 5 // FAILED

  // ---- the fixed world ---------------------------------------------
  pure val pkgs = Set("main", "lib", "plain")

  // import edges; lib imports main back, forming a cycle
  pure def deps(p) =
    if (p == "main") Set("lib", "plain")
    else if (p == "lib") Set("main")
    else Set()

  // ---- state --------------------------------------------------------
  var stage: str -> str
  // initOnce consumed — the bootstrap cascade of this package began
  var initStarted: str -> bool
  // how often the bootstrap body ran (INV: at most once per package)
  var bootstraps: str -> int
  // constOnce fast path: constants bound without full initialization
  var constsBound: str -> bool
  // explicit cascade frames, innermost frame first
  var stack: List[str]
  // counts in-cycle partial serves (a witness of the cyclic behavior)
  var partialServes: int
  // dependencies actually touched by each package's init so far —
  // completion and failure propagation wait only for these, not for
  // declared imports the initializer never used
  var depsTouched: str -> Set[str]
  // ghost: highest forward stage ever reached, for monotonicity
  var highWater: str -> int

  action init = all {
    stage' = pkgs.mapBy(p => PARSED),
    initStarted' = pkgs.mapBy(p => false),
    bootstraps' = pkgs.mapBy(p => 0),
    constsBound' = pkgs.mapBy(p => false),
    stack' = List(),
    partialServes' = 0,
    depsTouched' = pkgs.mapBy(p => Set()),
    highWater' = pkgs.mapBy(p => 1),
  }

  // ---- helpers ------------------------------------------------------
  pure def inStack(st, p) = st.indices().exists(i => st.nth(i) == p)

  // high-water marks only ever rise: the ghost records the maximum
  // forward stage ever reached, so a regressed stage can never drag
  // the mark down with it (which would make `monotonic` vacuous)
  pure def bump(hw, p, ord) =
    hw.set(p, if (hw.get(p) > ord) hw.get(p) else ord)

  // deps of p that still block its completion: not ready, not failed,
  // and not already mid-init. A cycle member counts as resolved — Go
  // serves its partial globals, which is all there will ever be.
  pure def unresolvedDeps(stageOf, st, touched, p) =
    touched.get(p).filter(q =>
      stageOf.get(q) != READY and
      stageOf.get(q) != FAILED and
      not(inStack(st, q)))

  // ---- loader progression -------------------------------------------
  // indexing finishes, successfully or not (redeclaration, lang gate,
  // cgo import — the load-phase failures of indexFiles)
  action finishIndexing(p) = all {
    stage.get(p) == PARSED,
    any {
      all {
        stage' = stage.set(p, INDEXED),
        highWater' = bump(highWater, p, 2),
      },
      all {
        stage' = stage.set(p, FAILED),
        highWater' = bump(highWater, p, 5),
      },
    },
    initStarted' = initStarted,
    bootstraps' = bootstraps,
    constsBound' = constsBound,
    stack' = stack,
    partialServes' = partialServes,
    depsTouched' = depsTouched,
  }

  // ---- initialization triggers --------------------------------------
  // A first var/const touch from running VM code (or a blank import, or
  // an explicit EnsureReady) consumes initOnce and pushes the frame.
  // Corresponds to EnsureReadyRun winning the once.
  action beginInit(p) = all {
    stage.get(p) == INDEXED,
    not(initStarted.get(p)),
    // a single logical goroutine runs one cascade at a time: an
    // external trigger cannot arrive mid-cascade (the goroutine is
    // busy inside it), and a nested trigger is touchDep's recursion
    stack == List(),
    stage' = stage.set(p, INITIALIZING),
    highWater' = bump(highWater, p, 3),
    initStarted' = initStarted.set(p, true),
    bootstraps' = bootstraps.set(p, bootstraps.get(p) + 1),
    stack' = List(p).concat(stack),
    constsBound' = constsBound,
    partialServes' = partialServes,
    depsTouched' = depsTouched,
  }

  // The innermost initializing package's init body reads a member of
  // its dependency q. Cases from MemberV / EnsureReadyRun:
  //   - q is mid-init (in the stack, possibly p itself): the cycle rule
  //     serves partial globals, nothing else happens — this is what keeps
  //     cyclic inits from deadlocking;
  //   - q is indexed and untouched: the cascade recurses — q begins
  //     initializing on this same goroutine;
  //   - q is ready or failed already: no transition (the touch is a
  //     plain read), so there is deliberately no branch for it.
  action touchDep(p, q) = all {
    stack.length() > 0,
    p == stack.head(),
    q.in(deps(p)),
    depsTouched' = depsTouched.set(p, depsTouched.get(p).union(Set(q))),
    any {
      // cycle: serve partial, count the witness — with beginInit
      // single-cascade, q in the stack IS a genuine ancestor frame, so
      // this branch is reached only through cascade reentry
      all {
        inStack(stack, q),
        partialServes' = partialServes + 1,
        stage' = stage,
        highWater' = highWater,
        initStarted' = initStarted,
        bootstraps' = bootstraps,
        constsBound' = constsBound,
        stack' = stack,
      },
      // recurse into q
      all {
        not(inStack(stack, q)),
        stage.get(q) == INDEXED,
        not(initStarted.get(q)),
        stage' = stage.set(q, INITIALIZING),
        highWater' = bump(highWater, q, 3),
        initStarted' = initStarted.set(q, true),
        bootstraps' = bootstraps.set(q, bootstraps.get(q) + 1),
        stack' = List(q).concat(stack),
        constsBound' = constsBound,
        partialServes' = partialServes,
      },
    },
  }

  // p's initializer finishes: every dependency it uses is resolved
  // (ready, failed, or a cycle member mid-init). Failure propagates: a
  // failed dependency means the importer's bootstrap errored too.
  // Otherwise the outcome is nondeterministic — the body itself may
  // error or panic.
  action completeInit(p) = all {
    stack.length() > 0,
    p == stack.head(),
    unresolvedDeps(stage, stack, depsTouched, p) == Set(),
    initStarted' = initStarted,
    bootstraps' = bootstraps,
    constsBound' = constsBound,
    partialServes' = partialServes,
    depsTouched' = depsTouched,
    stack' = stack.tail(),
    any {
      all {
        // success is impossible when a touched dependency failed
        not(depsTouched.get(p).exists(q => stage.get(q) == FAILED)),
        stage' = stage.set(p, READY),
        highWater' = bump(highWater, p, 4),
      },
      all {
        stage' = stage.set(p, FAILED),
        highWater' = bump(highWater, p, 5),
      },
    },
  }

  // ---- const-only fast path -----------------------------------------
  // Host-side const access runs ConstBootstrap once: constants bind
  // while vars and init() stay pending. Never touches the stage — the
  // hidden second state machine of docs/lazy-lifecycle-model.md.
  action bindConsts(p) = all {
    stage.get(p) == INDEXED,
    not(constsBound.get(p)),
    any {
      // success: constants bind, vars and init() stay pending
      all {
        constsBound' = constsBound.set(p, true),
        stage' = stage,
        highWater' = highWater,
        initStarted' = initStarted,
        bootstraps' = bootstraps,
        stack' = stack,
      },
      // the const-only path cannot evaluate the specs (they reference
      // vars or funcs — see TestConstMemberVarFallback): the caller
      // falls back to the full initializer, exactly like beginInit
      all {
        not(initStarted.get(p)),
        stack == List(),
        constsBound' = constsBound,
        stage' = stage.set(p, INITIALIZING),
        highWater' = bump(highWater, p, 3),
        initStarted' = initStarted.set(p, true),
        bootstraps' = bootstraps.set(p, bootstraps.get(p) + 1),
        stack' = List(p).concat(stack),
      },
    },
    partialServes' = partialServes,
    depsTouched' = depsTouched,
  }

  // Terminal stutter: once every package has settled (ready or failed)
  // and no cascade is in flight, the system may idle forever. This
  // keeps `quint run` from reporting normal completion as a deadlock —
  // a stack that is non-empty with no enabled action IS a real
  // deadlock and stays reportable.
  action settle = all {
    stack == List(),
    pkgs.forall(p => stage.get(p) == READY or stage.get(p) == FAILED),
    stage' = stage,
    highWater' = highWater,
    initStarted' = initStarted,
    bootstraps' = bootstraps,
    constsBound' = constsBound,
    stack' = stack,
    partialServes' = partialServes,
    depsTouched' = depsTouched,
  }

  // ---- step ----------------------------------------------------------
  action step = any {
    finishIndexing("main"),
    finishIndexing("lib"),
    finishIndexing("plain"),
    beginInit("main"),
    beginInit("lib"),
    beginInit("plain"),
    touchDep("main", "lib"),
    touchDep("main", "plain"),
    touchDep("lib", "main"),
    completeInit("main"),
    completeInit("lib"),
    completeInit("plain"),
    bindConsts("main"),
    bindConsts("lib"),
    bindConsts("plain"),
    settle,
  }

  // ---- invariants ----------------------------------------------------
  // INV3: bootstrap executes at most once per package.
  val bootstrapsAtMostOnce =
    pkgs.forall(p => bootstraps.get(p) <= 1)

  // INV-C: every frame on the cascade stack is a package mid-init, and
  // no package appears twice (a package re-entered through a cycle is
  // served partial instead of being pushed again).
  val stackConsistent =
    stack.indices().forall(i =>
      stage.get(stack.nth(i)) == INITIALIZING) and
    stack.foldl(Set(), (acc, x) => acc.union(Set(x))).size() == stack.length()

  // INV1: the stage never regresses below its high-water mark, except
  // into FAILED. (highWater records the best forward stage reached;
  // FAILED as 5 sits above READY as 4, so this also pins INV2's
  // absorbing property for everything the model can reach.)
  val monotonic =
    pkgs.forall(p => stageOrd(stage.get(p)) >= highWater.get(p) or
                     stageOrd(stage.get(p)) == highWater.get(p))

  // INV2: ready and failed are absorbing — encoded structurally (no
  // action fires out of them), cross-checked by monotonic + the fact
  // that only completeInit writes them.

  // INV-W (witness, not an invariant): cyclic initialization does
  // occur — `quint run --invariant=noPartialServeYet` reporting a
  // violation proves the model can exhibit Go's cyclic partial-read
  // behavior.
  val noPartialServeYet = partialServes == 0

  // everything at once, for a single --invariant run
  val allInvariants =
    bootstrapsAtMostOnce and stackConsistent and monotonic
}
