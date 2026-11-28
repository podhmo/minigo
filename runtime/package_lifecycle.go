package runtime

import (
	"fmt"

	"github.com/podhmo/minigo/internal/execution"
	"github.com/podhmo/minigo/internal/spike"
)

// State is a [Package]'s stage in loading, reported by [Package.State].
// A package moves forward through the stages in order; [Failed] means
// loading or initialization failed.
type State int

const (
	Parsed State = iota
	Indexed
	Initializing
	Ready
	Failed
)

type packageStateTransition struct {
	from State
	to   State
}

// packageStateTransitions is the production transcription of the Quint
// lifecycle in spec/lazypkg.qnt. Parsed→Ready is the host-package edge:
// a bound table has no source indexing or initializer. Failed→Initializing
// is valid only for a load-time failure, checked dynamically through
// initStarted; init-time failure is absorbing.
var packageStateTransitions = map[packageStateTransition]bool{
	{Parsed, Indexed}:       true,
	{Parsed, Ready}:         true,
	{Parsed, Failed}:        true,
	{Indexed, Initializing}: true,
	{Initializing, Ready}:   true,
	{Initializing, Failed}:  true,
	{Failed, Initializing}:  true,
}

// State reports the package's loading stage.
func (p *Package) State() State { return State(p.state.Load()) }

// CompleteIndexing records successful source indexing.
func (p *Package) CompleteIndexing() { p.advanceState(Indexed) }

// FailLoad records a parse or indexing failure.
func (p *Package) FailLoad() { p.advanceState(Failed) }

// MarkHostReady records publication of a host-bound package, which has
// no source indexing or initializer.
func (p *Package) MarkHostReady() { p.advanceState(Ready) }

// advanceState records a package lifecycle transition and panics on a
// transition the verified lifecycle forbids.
func (p *Package) advanceState(to State) {
	for {
		from := p.State()
		if !p.canAdvanceState(from, to) {
			panic(fmt.Sprintf("minigo: invalid package state transition %s -> %s", stateLabel(from), stateLabel(to)))
		}
		if p.state.CompareAndSwap(int32(from), int32(to)) {
			return
		}
	}
}

func (p *Package) canAdvanceState(from, to State) bool {
	if !packageStateTransitions[packageStateTransition{from, to}] {
		return false
	}
	if from == Failed && to == Initializing {
		return !p.initStarted.Load()
	}
	return true
}

func stateLabel(s State) string {
	switch s {
	case Parsed:
		return "Parsed"
	case Indexed:
		return "Indexed"
	case Initializing:
		return "Initializing"
	case Ready:
		return "Ready"
	case Failed:
		return "Failed"
	default:
		return fmt.Sprintf("State(%d)", int(s))
	}
}

// EnsureReady initializes the package — its var declarations and init
// functions — if it is not [Ready] yet, and returns the initialization
// error. Initialization runs at most once; a failure leaves the package
// [Failed] and is returned again by later calls. It runs on a fresh
// interpreter through RunInit; see [Package.EnsureReadyRun].
func (p *Package) EnsureReady() error {
	return p.EnsureReadyRun(nil)
}

// EnsureReadyRun is [Package.EnsureReady] with an explicit runner: run
// executes the package initializer, so code already running on an
// interpreter initializes the package on that same interpreter. A nil run
// falls back to RunInit.
func (p *Package) EnsureReadyRun(run func(*Function) error) error {
	p.WaitIndexed()
	return p.initOnce.Run(func() error {
		if p.State() == Ready {
			return nil
		}
		p.advanceState(Initializing)
		p.initOwner.Store(execution.GoroutineID())
		p.initStarted.Store(true)
		spike.NoteInitDuringCompile()
		if err := p.runBootstrap(run); err != nil {
			return err
		}
		p.advanceState(Ready)
		return nil
	}, func(error) { p.advanceState(Failed) })
}

// runBootstrap invokes p.Bootstrap with the init runner; [execution.Init.Run]
// around it records panics and marks the package Failed before
// re-panicking.
func (p *Package) runBootstrap(run func(*Function) error) error {
	if p.Bootstrap == nil {
		return nil
	}
	run, err := p.resolveInitRunner(run)
	if err != nil {
		return err
	}
	return p.Bootstrap(p, run)
}

func (p *Package) resolveInitRunner(run func(*Function) error) (func(*Function) error, error) {
	if run == nil {
		run = p.RunInit
	}
	if run == nil {
		return nil, fmt.Errorf("package %s: no init runner", p.Name)
	}
	return run, nil
}

// EnsureConstsRun binds the package's constants through ConstBootstrap,
// without running var initializers or init functions. It runs at most
// once and does nothing for a package already [Ready]. An error (also
// from a panic) only means the constants are not bound; the caller can
// fall back to [Package.EnsureReadyRun].
func (p *Package) EnsureConstsRun(run func(*Function) error) error {
	gid := execution.GoroutineID()
	if p.constOwner.Load() == gid {
		return fmt.Errorf("constant initialization cycle: %s", p.Name)
	}
	return p.constOnce.Try(func() error {
		if p.State() == Ready || p.ConstBootstrap == nil {
			return nil
		}
		run, err := p.resolveInitRunner(run)
		if err != nil {
			return err
		}
		spike.NoteInitDuringCompile()
		p.constOwner.Store(gid)
		defer p.constOwner.Store(0)
		return p.ConstBootstrap(p, run)
	})
}
