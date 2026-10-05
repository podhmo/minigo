package main

import (
	"errors"
	"io"
	"os"
	"strings"

	"github.com/podhmo/minigo/runtime"
)

// dryCmd stands in for *exec.Cmd under -n. The VM reaches host values
// through reflection, so a Taskfile that builds a command with
// exec.Command, sets its fields, and calls Run/Output/... sees the same
// surface — but the "run" prints the command line instead of spawning it.
// Printing at run time (not at Command time) picks up a Dir set after
// construction and skips commands that are built but never run.
type dryCmd struct {
	Path   string
	Args   []string
	Env    []string
	Dir    string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	Process      *os.Process
	ProcessState *os.ProcessState

	r       *Runner
	started bool
}

func (c *dryCmd) echo() {
	var name string
	var args []string
	if len(c.Args) > 0 {
		name, args = c.Args[0], c.Args[1:]
	}
	c.r.echo(cmdLabel(c.Dir, name, args))
}

func (c *dryCmd) Run() error {
	if c.started {
		return errors.New("exec: already started")
	}
	c.started = true
	c.echo()
	return nil
}

func (c *dryCmd) Start() error { return c.Run() }

func (c *dryCmd) Wait() error {
	if !c.started {
		return errors.New("exec: not started")
	}
	return nil
}

// Output and CombinedOutput return no bytes: there is no truthful output
// without running the command (like task.Output under -n).
func (c *dryCmd) Output() ([]byte, error) {
	if c.Stdout != nil {
		return nil, errors.New("exec: Stdout already set")
	}
	return []byte{}, c.Run()
}

func (c *dryCmd) CombinedOutput() ([]byte, error) {
	if c.Stdout != nil {
		return nil, errors.New("exec: Stdout already set")
	}
	if c.Stderr != nil {
		return nil, errors.New("exec: Stderr already set")
	}
	return []byte{}, c.Run()
}

func (c *dryCmd) String() string { return strings.Join(c.Args, " ") }

// Environ mirrors (*exec.Cmd).Environ for scripts that extend the env.
func (c *dryCmd) Environ() []string {
	if c.Env != nil {
		return c.Env
	}
	return os.Environ()
}

// dryExecCommand replaces exec.Command under -n. Dir starts empty, like
// the real binding's cwd default: cmdLabel then omits the "(in dir)"
// prefix for commands that run in the Taskfile's directory.
func (r *Runner) dryExecCommand(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) == 0 {
		return nil, errors.New("exec.Command needs a name")
	}
	name, err := strArg("exec.Command", args, 0)
	if err != nil {
		return nil, err
	}
	argv, err := strArgs("exec.Command", args[1:], 2)
	if err != nil {
		return nil, err
	}
	return &runtime.GoValue{V: &dryCmd{Path: name, Args: append([]string{name}, argv...), r: r}}, nil
}
