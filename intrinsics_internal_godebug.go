package minigo

import (
	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installInternalGodebug(h *hostHelpers) {
	// net/url needs no intrinsic: its source interprets cleanly once
	// the internal/godebug stub answers the knob lookups below.
	// internal/godebug cannot be imported outside GOROOT, so a stub
	// Setting answers the knobs stdlib sources consult: Value() reports
	// the setting's GODEBUG entry, or "" when unset — every gate
	// defaults to its enabled behavior (e.g. url's query-param limit
	// check in ParseQuery), and a t.Setenv-style GODEBUG write made
	// through internal/testenv.SetGODEBUG below is honored.
	e.Bind("internal/godebug", map[string]runtime.Value{
		"New": h.fn1("godebug.New", func(a []any) (any, error) {
			return &runtime.GoValue{V: &godebugSetting{name: str(a[0])}}, nil
		}),
		"Setting": hostType("internal/godebug.Setting", func() any { return &godebugSetting{} }),
	})
}

// godebugSetting stands in for internal/godebug.Setting (which cannot be
// imported outside GOROOT): it answers the methods stdlib sources call
// on their godebug knobs. The real implementation syncs a cache through
// a runtime hook on env changes; reading $GODEBUG on each Value() call
// reports the same current value — "" when the knob is unset, which is
// the enabled-by-default answer every consumer relies on.
type godebugSetting struct{ name string }
