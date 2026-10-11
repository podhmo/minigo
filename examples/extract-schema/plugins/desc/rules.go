// Package descrules is the second extract-schema plugin example: a
// DescRules-only plugin (no Overrides) carrying the description-filter
// rules the walk applies to every doc comment it emits.
//
// Rules are plain data: op names the script interprets.
//
//	"strip-symbol-prefix" — drop a leading `<SymbolName>`/`SymbolName:`
//	    (the Go doc convention, e.g. "// User is ..." -> "is ...")
//	"strip-prefix:<text>" — drop the literal prefix
//	"replace:<old>-><new>" — ReplaceAll(old -> new)
//
// Rules travel as plain strings: they cross the host->script boundary
// unboxed (only []string/[]byte do), keep their order, and stay
// self-describing in `go doc`.
package descrules

// DescRules returns the rules applied, in order, to each description
// the generator emits (type docs and field docs alike).
func DescRules() []string {
	return []string{
		"strip-symbol-prefix",
	}
}
