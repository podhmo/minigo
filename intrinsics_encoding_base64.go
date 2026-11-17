package minigo

import (
	"encoding/base64"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installEncodingBase64(h *hostHelpers) {
	e.Bind("encoding/base64", map[string]runtime.Value{
		"StdEncoding":    &runtime.GoValue{V: base64.StdEncoding},
		"URLEncoding":    &runtime.GoValue{V: base64.URLEncoding},
		"RawStdEncoding": &runtime.GoValue{V: base64.RawStdEncoding},
		"RawURLEncoding": &runtime.GoValue{V: base64.RawURLEncoding},
	})
}
