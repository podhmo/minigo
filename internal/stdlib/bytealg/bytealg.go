package bytealg

import (
	"bytes"
	goruntime "runtime"
	"strings"

	"github.com/podhmo/minigo/internal/stdlib"

	"github.com/podhmo/minigo/runtime"
)

// Install binds the internal/bytealg stub surface (pure helpers GOROOT sources call).
func Install(bind stdlib.Bind, h stdlib.Helpers) {
	// internal/bytealg and internal/stringslite back strings/bytes in
	// GOROOT, and their sources lean on unsafe (bytealg's init reads
	// unsafe.Offsetof of cpu flags). Stub the pure entry points stdlib
	// sources call — embed.FS's lookup, for one — over strings/bytes.
	bind("internal/bytealg", map[string]runtime.Value{
		"MaxLen":        int64(MaxLen()),
		"MaxBruteForce": int64(MaxBruteForce()),
		"Compare": h.Fn2("bytealg.Compare", func(a []any) (any, error) {
			return int(bytes.Compare(stdlib.ByteSlice(a[0]), stdlib.ByteSlice(a[1]))), nil
		}, bytes.Compare),
		"Count": h.Fn2("bytealg.Count", func(a []any) (any, error) {
			return int(bytes.Count(stdlib.ByteSlice(a[0]), []byte{byte(stdlib.Int64Of(a[1]))})), nil
		}),
		"CountString": h.Fn2("bytealg.CountString", func(a []any) (any, error) {
			return int(strings.Count(stdlib.Str(a[0]), string([]byte{byte(stdlib.Int64Of(a[1]))}))), nil
		}),
		"Equal": h.Fn2("bytealg.Equal", func(a []any) (any, error) { return bytes.Equal(stdlib.ByteSlice(a[0]), stdlib.ByteSlice(a[1])), nil }, bytes.Equal),
		"Index": h.Fn2("bytealg.Index", func(a []any) (any, error) {
			return int(bytes.Index(stdlib.ByteSlice(a[0]), stdlib.ByteSlice(a[1]))), nil
		}, bytes.Index),
		"IndexString": h.Fn2("bytealg.IndexString", func(a []any) (any, error) { return int(strings.Index(stdlib.Str(a[0]), stdlib.Str(a[1]))), nil }, strings.Index),
		"IndexByte": h.Fn2("bytealg.IndexByte", func(a []any) (any, error) {
			return int(bytes.IndexByte(stdlib.ByteSlice(a[0]), byte(stdlib.Int64Of(a[1])))), nil
		}, bytes.IndexByte),
		"IndexByteString": h.Fn2("bytealg.IndexByteString", func(a []any) (any, error) {
			return int(strings.IndexByte(stdlib.Str(a[0]), byte(stdlib.Int64Of(a[1])))), nil
		}, strings.IndexByte),
		"LastIndexByte": h.Fn2("bytealg.LastIndexByte", func(a []any) (any, error) {
			return int(bytes.LastIndexByte(stdlib.ByteSlice(a[0]), byte(stdlib.Int64Of(a[1])))), nil
		}, bytes.LastIndexByte),
		"LastIndexByteString": h.Fn2("bytealg.LastIndexByteString", func(a []any) (any, error) {
			return int(strings.LastIndexByte(stdlib.Str(a[0]), byte(stdlib.Int64Of(a[1])))), nil
		}, strings.LastIndexByte),
		"CompareString": h.Fn2("bytealg.CompareString", func(a []any) (any, error) { return strings.Compare(stdlib.Str(a[0]), stdlib.Str(a[1])), nil }, strings.Compare),
		// gc's Cutover is an arch-tuned threshold: IndexByte calls it to
		// decide when too many false-positive hits warrant switching to
		// Index. Either search path returns the same offset, so a small
		// constant preserves observable behavior.
		"Cutover": h.Fn1("bytealg.Cutover", func(a []any) (any, error) { return int(2), nil }),
		// gc hands the runtime a zeroed []byte of len n to fill; a script
		// []byte of zeroed elements does the same for the callers (bytes's
		// Join/Repeat/ToUpper, strings.Builder's grow).
		"MakeNoZero": h.Fn1("bytealg.MakeNoZero", func(a []any) (any, error) {
			return make([]byte, int(stdlib.Int64Of(a[0]))), nil
		}),
	})
}

// bytealgMaxLen mirrors the host build's arch-tuned init (internal/bytealg
// index_<arch>.go). amd64 is 63 with AVX2 else 31; without reading CPUID we
// take the conservative 31 — the bound only gates when IndexString may be
// used, and a smaller bound just sends longer needles to the generic path.
// arm64/ppc64x 32, s390x/loong64 64.
func MaxLen() int {
	switch goruntime.GOARCH {
	case "amd64":
		return 31
	case "arm64", "ppc64", "ppc64le":
		return 32
	case "s390x", "loong64":
		return 64
	}
	return 0
}

// bytealgMaxBruteForce mirrors the host build's arch-tuned constant
// (internal/bytealg/index_<arch>.go): amd64 and s390x brute-force up to
// 64, arm64/ppc64x/loong64 up to 16, generic 0.
func MaxBruteForce() int {
	switch goruntime.GOARCH {
	case "amd64", "s390x":
		return 64
	case "arm64", "ppc64", "ppc64le", "loong64":
		return 16
	}
	return 0
}
