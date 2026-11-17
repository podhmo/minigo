package minigo

import (
	"crypto/sha256"
	"go/constant"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installCryptoSha256(h *hostHelpers) {
	// crypto/sha256: Sum256's [32]byte result unboxes element-wise so
	// `sum[:]` slices and hex.EncodeToString consume it.
	e.Bind("crypto/sha256", map[string]runtime.Value{
		"New": h.fn("sha256.New", func(a []any) (any, error) {
			return &runtime.GoValue{V: sha256.New()}, nil
		}, sha256.New),
		"New224": h.fn("sha256.New224", func(a []any) (any, error) {
			return &runtime.GoValue{V: sha256.New224()}, nil
		}, sha256.New224),
		"Sum256": h.fn1("sha256.Sum256", func(a []any) (any, error) {
			sum := sha256.Sum256(byteSlice(a[0]))
			return scriptVal(sum[:]), nil
		}, sha256.Sum256),
		"Sum224": h.fn1("sha256.Sum224", func(a []any) (any, error) {
			sum := sha256.Sum224(byteSlice(a[0]))
			return scriptVal(sum[:]), nil
		}, sha256.Sum224),
		"Size":      &runtime.UConst{V: constant.MakeInt64(sha256.Size)},
		"Size224":   &runtime.UConst{V: constant.MakeInt64(sha256.Size224)},
		"BlockSize": &runtime.UConst{V: constant.MakeInt64(sha256.BlockSize)},
	})
}
