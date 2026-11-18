//go:generate go run ./internal/cmd/genbind

package minigo

func (e *Engine) installEncodingHex(h *hostHelpers) {
	e.Bind("encoding/hex", encodingHexGenEntries())
}
