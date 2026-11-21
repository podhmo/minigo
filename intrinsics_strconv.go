package minigo

import (
	"go/constant"
	"strconv"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installStrconv(h *hostHelpers) {
	e.Bind("strconv", map[string]runtime.Value{
		"Atoi":    h.fn("strconv.Atoi", func(a []any) (any, error) { return retErr2(strconv.Atoi(str(a[0]))) }),
		"Itoa":    h.fn("strconv.Itoa", func(a []any) (any, error) { return strconv.Itoa(intOf(a[0])), nil }, strconv.Itoa),
		"Quote":   h.fn("strconv.Quote", func(a []any) (any, error) { return strconv.Quote(str(a[0])), nil }, strconv.Quote),
		"Unquote": h.fn("strconv.Unquote", func(a []any) (any, error) { return retErr2(strconv.Unquote(str(a[0]))) }),
		"UnquoteChar": h.fn2("strconv.UnquoteChar", func(a []any) (any, error) {
			r, multibyte, tail, err := strconv.UnquoteChar(str(a[0]), byte(int64Of(a[1])))
			return &runtime.Tuple{Elems: []runtime.Value{namedSized(r, int64(r)), multibyte, tail, errVal(err)}}, nil
		}),
		"ParseUint": h.fn3("strconv.ParseUint", func(a []any) (any, error) {
			return retErr2(strconv.ParseUint(str(a[0]), intOf(a[1]), intOf(a[2])))
		}),
		"FormatFloat": h.fn3("strconv.FormatFloat", func(a []any) (any, error) {
			f, _ := a[0].(float64)
			return strconv.FormatFloat(f, byte(intOf(a[1])), intOf(a[2]), 64), nil
		}),
		"FormatBool": h.fn("strconv.FormatBool", func(a []any) (any, error) {
			b, _ := a[0].(bool)
			return strconv.FormatBool(b), nil
		}),
		"ParseInt": h.fn3("strconv.ParseInt", func(a []any) (any, error) { return retErr2(strconv.ParseInt(str(a[0]), intOf(a[1]), intOf(a[2]))) }),
		"ParseFloat": h.fn2("strconv.ParseFloat", func(a []any) (any, error) {
			return retErr2(strconv.ParseFloat(str(a[0]), intOf(a[1])))
		}),
		"ParseBool": h.fn("strconv.ParseBool", func(a []any) (any, error) { return retErr2(strconv.ParseBool(str(a[0]))) }),
		"FormatInt": h.fn2("strconv.FormatInt", func(a []any) (any, error) { return strconv.FormatInt(int64Of(a[0]), intOf(a[1])), nil }, strconv.FormatInt),
		"FormatUint": h.fn2("strconv.FormatUint", func(a []any) (any, error) {
			return strconv.FormatUint(uint64(int64Of(a[0])), intOf(a[1])), nil
		}, strconv.FormatUint),
		"QuoteToASCII": h.fn("strconv.QuoteToASCII", func(a []any) (any, error) { return strconv.QuoteToASCII(str(a[0])), nil }, strconv.QuoteToASCII),
		"QuoteRune":    h.fn("strconv.QuoteRune", func(a []any) (any, error) { return strconv.QuoteRune(runeOf(a[0])), nil }, strconv.QuoteRune),
		"IsPrint":      h.fn("strconv.IsPrint", func(a []any) (any, error) { return strconv.IsPrint(runeOf(a[0])), nil }, strconv.IsPrint),
		"IsGraphic":    h.fn("strconv.IsGraphic", func(a []any) (any, error) { return strconv.IsGraphic(runeOf(a[0])), nil }, strconv.IsGraphic),
		"CanBackquote": h.fn("strconv.CanBackquote", func(a []any) (any, error) { return strconv.CanBackquote(str(a[0])), nil }, strconv.CanBackquote),
		// The Append family appends to dst and returns the extended slice;
		// callers always use the return value, so crossing dst as a host
		// copy is faithful (net/http's writeStatusLine builds into a
		// scratch buffer this way).
		"AppendBool": h.fn2("strconv.AppendBool", func(a []any) (any, error) {
			b, _ := a[1].(bool)
			return strconv.AppendBool(byteSlice(a[0]), b), nil
		}),
		"AppendInt": h.fn3("strconv.AppendInt", func(a []any) (any, error) {
			return strconv.AppendInt(byteSlice(a[0]), int64Of(a[1]), intOf(a[2])), nil
		}),
		"AppendUint": h.fn3("strconv.AppendUint", func(a []any) (any, error) {
			return strconv.AppendUint(byteSlice(a[0]), uint64(int64Of(a[1])), intOf(a[2])), nil
		}, strconv.AppendUint),
		"AppendFloat": h.fn("strconv.AppendFloat", func(a []any) (any, error) {
			f, _ := a[1].(float64)
			return strconv.AppendFloat(byteSlice(a[0]), f, byte(intOf(a[2])), intOf(a[3]), intOf(a[4])), nil
		}),
		"AppendQuote": h.fn2("strconv.AppendQuote", func(a []any) (any, error) {
			return strconv.AppendQuote(byteSlice(a[0]), str(a[1])), nil
		}),
		"AppendQuoteToASCII": h.fn2("strconv.AppendQuoteToASCII", func(a []any) (any, error) {
			return strconv.AppendQuoteToASCII(byteSlice(a[0]), str(a[1])), nil
		}),
		"AppendQuoteRune": h.fn2("strconv.AppendQuoteRune", func(a []any) (any, error) {
			return strconv.AppendQuoteRune(byteSlice(a[0]), runeOf(a[1])), nil
		}),
		"AppendQuoteRuneToASCII": h.fn2("strconv.AppendQuoteRuneToASCII", func(a []any) (any, error) {
			return strconv.AppendQuoteRuneToASCII(byteSlice(a[0]), runeOf(a[1])), nil
		}),
		// the host's value is the architecture-appropriate one (64-bit
		// only build); interpreted stdlib code reaches it, e.g.
		// encoding/base64's decoder.
		"IntSize": &runtime.UConst{V: constant.MakeInt64(strconv.IntSize)},
		// sentinels ride GoValue boxes so `err == strconv.ErrSyntax` and
		// errors.Is against the chain see the real objects; *NumError
		// already arrives boxed from the parse functions and unwraps to
		// them natively. The NumError typedef makes `var e *strconv.NumError`
		// (an errors.As target) resolvable.
		"ErrSyntax": &runtime.GoValue{V: strconv.ErrSyntax},
		"ErrRange":  &runtime.GoValue{V: strconv.ErrRange},
		"NumError":  hostType("strconv.NumError", func() any { return &strconv.NumError{} }),
	})
}
