package minigo

import (
	"regexp"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installRegexp(h *hostHelpers) {
	e.Bind("regexp", map[string]runtime.Value{
		// the Regexp type, so `x.(*regexp.Regexp)` and `*regexp.Regexp`
		// slots resolve to the host type the constructors return.
		"Regexp":      hostType("regexp.Regexp", func() any { return &regexp.Regexp{} }),
		"Compile":     h.fn("regexp.Compile", func(a []any) (any, error) { return retErr2(regexp.Compile(str(a[0]))) }),
		"MustCompile": h.fn("regexp.MustCompile", func(a []any) (any, error) { return regexp.MustCompile(str(a[0])), nil }, regexp.MustCompile),
		"MatchString": h.fn2("regexp.MatchString", func(a []any) (any, error) { return retErr2(regexp.MatchString(str(a[0]), str(a[1]))) }),
		"Match":       h.fn2("regexp.Match", func(a []any) (any, error) { return retErr2(regexp.Match(str(a[0]), byteSlice(a[1]))) }),
		"QuoteMeta":   h.fn("regexp.QuoteMeta", func(a []any) (any, error) { return regexp.QuoteMeta(str(a[0])), nil }, regexp.QuoteMeta),
	})
}
