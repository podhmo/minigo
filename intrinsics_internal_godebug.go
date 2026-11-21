package minigo

import (
	"os"
	"strings"

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

func (s *godebugSetting) Value() string {
	// a name may repeat in GODEBUG — gc's runtime parse keeps the LAST
	// entry, which is also what makes testenv.SetGODEBUG's appended
	// override win over an earlier setting.
	var val string
	for _, kv := range strings.Split(os.Getenv("GODEBUG"), ",") {
		if n, v, ok := strings.Cut(kv, "="); ok && n == s.Name() {
			val = v
		}
	}
	return val
}

func (s *godebugSetting) Name() string {
	if s.name != "" && s.name[0] == '#' {
		return s.name[1:]
	}
	return s.name
}

func (s *godebugSetting) Undocumented() bool { return s.name != "" && s.name[0] == '#' }

func (s *godebugSetting) String() string { return s.Name() + "=" + s.Value() }

func (s *godebugSetting) IncNonDefault() {}
