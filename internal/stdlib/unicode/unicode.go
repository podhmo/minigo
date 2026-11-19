package unicode

import (
	"unicode"

	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
)

// Install binds unicode: the generated table plus the case constants,
// spelled as ints for scripts (typed unicode.Case in source).
func Install(bind stdlib.Bind) {
	tbl := unicodeGenEntries()
	// the case constants are typed (unicode.Case) in source but scripts
	// spell them as ints — keep the plain int64 view.
	tbl["UpperCase"] = int64(unicode.UpperCase)
	tbl["LowerCase"] = int64(unicode.LowerCase)
	tbl["TitleCase"] = int64(unicode.TitleCase)
	tbl["MaxCase"] = int64(unicode.MaxCase)
	// the named case tables are exported vars (values of the case-table
	// types) — scripts pass them to unicode.SpecialCase().ToUpper etc.
	tbl["TurkishCase"] = &runtime.GoValue{V: unicode.TurkishCase}
	tbl["AzeriCase"] = &runtime.GoValue{V: unicode.AzeriCase}
	bind("unicode", tbl)
}
