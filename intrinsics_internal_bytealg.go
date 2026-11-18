package minigo

import (
	"bytes"
	"strings"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installInternalBytealg(h *hostHelpers) {
	// internal/bytealg and internal/stringslite back strings/bytes in
	// GOROOT, and their sources lean on unsafe (bytealg's init reads
	// unsafe.Offsetof of cpu flags). Stub the pure entry points stdlib
	// sources call — embed.FS's lookup, for one — over strings/bytes.
	e.bindBound("internal/bytealg", map[string]runtime.Value{
		"MaxLen":        int64(bytealgMaxLen()),
		"MaxBruteForce": int64(bytealgMaxBruteForce()),
		"Compare":       h.fn2("bytealg.Compare", func(a []any) (any, error) { return int(bytes.Compare(byteSlice(a[0]), byteSlice(a[1]))), nil }),
		"Count": h.fn2("bytealg.Count", func(a []any) (any, error) {
			return int(bytes.Count(byteSlice(a[0]), []byte{byte(int64Of(a[1]))})), nil
		}),
		"CountString": h.fn2("bytealg.CountString", func(a []any) (any, error) {
			return int(strings.Count(str(a[0]), string([]byte{byte(int64Of(a[1]))}))), nil
		}),
		"Equal":           h.fn2("bytealg.Equal", func(a []any) (any, error) { return bytes.Equal(byteSlice(a[0]), byteSlice(a[1])), nil }),
		"Index":           h.fn2("bytealg.Index", func(a []any) (any, error) { return int(bytes.Index(byteSlice(a[0]), byteSlice(a[1]))), nil }),
		"IndexString":     h.fn2("bytealg.IndexString", func(a []any) (any, error) { return int(strings.Index(str(a[0]), str(a[1]))), nil }),
		"IndexByte":       h.fn2("bytealg.IndexByte", func(a []any) (any, error) { return int(bytes.IndexByte(byteSlice(a[0]), byte(int64Of(a[1])))), nil }),
		"IndexByteString": h.fn2("bytealg.IndexByteString", func(a []any) (any, error) { return int(strings.IndexByte(str(a[0]), byte(int64Of(a[1])))), nil }),
		"LastIndexByte": h.fn2("bytealg.LastIndexByte", func(a []any) (any, error) {
			return int(bytes.LastIndexByte(byteSlice(a[0]), byte(int64Of(a[1])))), nil
		}),
		"LastIndexByteString": h.fn2("bytealg.LastIndexByteString", func(a []any) (any, error) { return int(strings.LastIndexByte(str(a[0]), byte(int64Of(a[1])))), nil }),
		"CompareString":       h.fn2("bytealg.CompareString", func(a []any) (any, error) { return strings.Compare(str(a[0]), str(a[1])), nil }),
		// gc's Cutover is an arch-tuned threshold: IndexByte calls it to
		// decide when too many false-positive hits warrant switching to
		// Index. Either search path returns the same offset, so a small
		// constant preserves observable behavior.
		"Cutover": h.fn1("bytealg.Cutover", func(a []any) (any, error) { return int(2), nil }),
		// gc hands the runtime a zeroed []byte of len n to fill; a script
		// []byte of zeroed elements does the same for the callers (bytes's
		// Join/Repeat/ToUpper, strings.Builder's grow).
		"MakeNoZero": h.fn1("bytealg.MakeNoZero", func(a []any) (any, error) {
			return make([]byte, int(int64Of(a[0]))), nil
		}),
	})
}
