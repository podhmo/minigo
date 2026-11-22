package regexp

import (
	"regexp"

	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
)

// Install binds the regexp intrinsics: the Regexp host type plus
// Compile/MustCompile/Match*/QuoteMeta.
func Install(bind stdlib.Bind, h stdlib.Helpers) {
	bind("regexp", map[string]runtime.Value{
		// the Regexp type, so `x.(*regexp.Regexp)` and `*regexp.Regexp`
		// slots resolve to the host type the constructors return.
		"Regexp":      stdlib.HostType("regexp.Regexp", func() any { return &regexp.Regexp{} }),
		"Compile":     h.Fn("regexp.Compile", func(a []any) (any, error) { return stdlib.RetErr2(regexp.Compile(stdlib.Str(a[0]))) }, regexp.Compile),
		"MustCompile": h.Fn("regexp.MustCompile", func(a []any) (any, error) { return regexp.MustCompile(stdlib.Str(a[0])), nil }, regexp.MustCompile),
		"MatchString": h.Fn2("regexp.MatchString", func(a []any) (any, error) {
			return stdlib.RetErr2(regexp.MatchString(stdlib.Str(a[0]), stdlib.Str(a[1])))
		}, regexp.MatchString),
		"Match": h.Fn2("regexp.Match", func(a []any) (any, error) {
			return stdlib.RetErr2(regexp.Match(stdlib.Str(a[0]), stdlib.ByteSlice(a[1])))
		}, regexp.Match),
		"QuoteMeta": h.Fn("regexp.QuoteMeta", func(a []any) (any, error) { return regexp.QuoteMeta(stdlib.Str(a[0])), nil }, regexp.QuoteMeta),
	})
}
