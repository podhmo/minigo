package minigo

import (
	"go/constant"
	"unicode"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installUnicode(h *hostHelpers) {
	e.Bind("unicode", map[string]runtime.Value{
		"IsControl": h.fn("unicode.IsControl", func(a []any) (any, error) { return unicode.IsControl(runeOf(a[0])), nil }, unicode.IsControl),
		"IsDigit":   h.fn("unicode.IsDigit", func(a []any) (any, error) { return unicode.IsDigit(runeOf(a[0])), nil }, unicode.IsDigit),
		"IsGraphic": h.fn("unicode.IsGraphic", func(a []any) (any, error) { return unicode.IsGraphic(runeOf(a[0])), nil }, unicode.IsGraphic),
		"IsLetter":  h.fn("unicode.IsLetter", func(a []any) (any, error) { return unicode.IsLetter(runeOf(a[0])), nil }, unicode.IsLetter),
		"IsLower":   h.fn("unicode.IsLower", func(a []any) (any, error) { return unicode.IsLower(runeOf(a[0])), nil }, unicode.IsLower),
		"IsMark":    h.fn("unicode.IsMark", func(a []any) (any, error) { return unicode.IsMark(runeOf(a[0])), nil }, unicode.IsMark),
		"IsNumber":  h.fn("unicode.IsNumber", func(a []any) (any, error) { return unicode.IsNumber(runeOf(a[0])), nil }, unicode.IsNumber),
		"IsPrint":   h.fn("unicode.IsPrint", func(a []any) (any, error) { return unicode.IsPrint(runeOf(a[0])), nil }, unicode.IsPrint),
		"IsPunct":   h.fn("unicode.IsPunct", func(a []any) (any, error) { return unicode.IsPunct(runeOf(a[0])), nil }, unicode.IsPunct),
		"IsSpace":   h.fn("unicode.IsSpace", func(a []any) (any, error) { return unicode.IsSpace(runeOf(a[0])), nil }, unicode.IsSpace),
		"IsSymbol":  h.fn("unicode.IsSymbol", func(a []any) (any, error) { return unicode.IsSymbol(runeOf(a[0])), nil }, unicode.IsSymbol),
		"IsTitle":   h.fn("unicode.IsTitle", func(a []any) (any, error) { return unicode.IsTitle(runeOf(a[0])), nil }, unicode.IsTitle),
		"IsUpper":   h.fn("unicode.IsUpper", func(a []any) (any, error) { return unicode.IsUpper(runeOf(a[0])), nil }, unicode.IsUpper),
		"ToLower":   h.fn("unicode.ToLower", func(a []any) (any, error) { return unicode.ToLower(runeOf(a[0])), nil }),
		"ToUpper":   h.fn("unicode.ToUpper", func(a []any) (any, error) { return unicode.ToUpper(runeOf(a[0])), nil }),
		"ToTitle":   h.fn("unicode.ToTitle", func(a []any) (any, error) { return unicode.ToTitle(runeOf(a[0])), nil }),
		"To":        h.fn2("unicode.To", func(a []any) (any, error) { return unicode.To(intOf(a[0]), runeOf(a[1])), nil }),
		// RangeTable membership checks — encoding/xml's isName builds its
		// own tables and calls these.
		"Is": h.fn2("unicode.Is", func(a []any) (any, error) {
			return unicode.Is(goNative(a[0]).(*unicode.RangeTable), runeOf(a[1])), nil
		}, unicode.Is),
		"In": h.fn2("unicode.In", func(a []any) (any, error) {
			var tabs []*unicode.RangeTable
			for _, t := range a[1:] {
				tabs = append(tabs, goNative(t).(*unicode.RangeTable))
			}
			return unicode.In(runeOf(a[0]), tabs...), nil
		}),
		"UpperCase": int64(unicode.UpperCase), "LowerCase": int64(unicode.LowerCase), "TitleCase": int64(unicode.TitleCase),
		// these consts are untyped in Go — they adapt to an operand's
		// declared type like a literal, so they ride as UConst.
		"MaxRune":         &runtime.UConst{V: constant.MakeInt64(unicode.MaxRune), Rune: true},
		"MaxASCII":        &runtime.UConst{V: constant.MakeInt64(unicode.MaxASCII), Rune: true},
		"ReplacementChar": &runtime.UConst{V: constant.MakeInt64(unicode.ReplacementChar), Rune: true},
		// stdlib code (encoding/xml's init) builds RangeTables as composite
		// literals — bind the range structs as host types.
		"RangeTable": hostType("unicode.RangeTable", func() any { return &unicode.RangeTable{} }),
		"Range16":    hostType("unicode.Range16", func() any { return &unicode.Range16{} }),
		"Range32":    hostType("unicode.Range32", func() any { return &unicode.Range32{} }),
		"CaseRange":  hostType("unicode.CaseRange", func() any { return &unicode.CaseRange{} }),
	})
}

// runePred adapts a script `func(rune) bool` to the host signature for
// strings.*Func calls; a panic inside the callback propagates through
// the host call like Go's.
func runePred(v runtime.ScriptCaller, fn runtime.Value) func(rune) bool {
	return func(r rune) bool {
		res := callOrPanic(v, fn, []runtime.Value{int64(r)})
		b, _ := res.(bool)
		return b
	}
}
