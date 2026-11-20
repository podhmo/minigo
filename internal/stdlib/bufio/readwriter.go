package bufio

import (
	"bufio"
	"fmt"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
)

// gc's params are *bufio.Reader/*bufio.Writer, not the io interfaces —
// anything else is a type error there.
var newReadWriterBind = &runtime.BuiltinFunc{Name: "bufio.NewReadWriter", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("bufio.NewReadWriter needs 2 args, got %d", len(args))
	}
	br, ok := hostbridge.GoNative(args[0]).(*bufio.Reader)
	if !ok {
		return nil, fmt.Errorf("bufio.NewReadWriter needs a *bufio.Reader, got %s", runtime.DisplayName(vc.TypeOf(args[0])))
	}
	bw, ok := hostbridge.GoNative(args[1]).(*bufio.Writer)
	if !ok {
		return nil, fmt.Errorf("bufio.NewReadWriter needs a *bufio.Writer, got %s", runtime.DisplayName(vc.TypeOf(args[1])))
	}
	return &runtime.GoValue{V: bufio.NewReadWriter(br, bw)}, nil
}}
