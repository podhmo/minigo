package stdjson

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
)

// Indent/Compact take a *bytes.Buffer destination like gc — a generic
// io.Writer would diverge, so the box must carry one.

var indentBind = &runtime.BuiltinFunc{Name: "json.Indent", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) != 4 {
		return nil, fmt.Errorf("json.Indent needs 4 args, got %d", len(args))
	}
	dst, ok := hostbridge.GoNative(args[0]).(*bytes.Buffer)
	if !ok {
		return nil, fmt.Errorf("json.Indent needs a *bytes.Buffer dst, got %s", runtime.DisplayName(v.TypeOf(args[0])))
	}
	err := json.Indent(dst, stdlib.ByteSlice(hostbridge.GoNative(args[1])),
		stdlib.Str(hostbridge.GoNative(args[2])), stdlib.Str(hostbridge.GoNative(args[3])))
	return stdlib.ErrVal(err), nil
}}

var compactBind = &runtime.BuiltinFunc{Name: "json.Compact", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("json.Compact needs 2 args, got %d", len(args))
	}
	dst, ok := hostbridge.GoNative(args[0]).(*bytes.Buffer)
	if !ok {
		return nil, fmt.Errorf("json.Compact needs a *bytes.Buffer dst, got %s", runtime.DisplayName(v.TypeOf(args[0])))
	}
	err := json.Compact(dst, stdlib.ByteSlice(hostbridge.GoNative(args[1])))
	return stdlib.ErrVal(err), nil
}}
