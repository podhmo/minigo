package minigo

import (
	"encoding/hex"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installEncodingHex(h *hostHelpers) {
	e.Bind("encoding/hex", map[string]runtime.Value{
		"EncodeToString": h.fn("hex.EncodeToString", func(a []any) (any, error) { return hex.EncodeToString(byteSlice(a[0])), nil }, hex.EncodeToString),
		"DecodeString":   h.fn("hex.DecodeString", func(a []any) (any, error) { return retErr2(hex.DecodeString(str(a[0]))) }),
		"EncodedLen":     h.fn("hex.EncodedLen", func(a []any) (any, error) { return hex.EncodedLen(intOf(a[0])), nil }),
		"DecodedLen":     h.fn("hex.DecodedLen", func(a []any) (any, error) { return hex.DecodedLen(intOf(a[0])), nil }),
	})
}
