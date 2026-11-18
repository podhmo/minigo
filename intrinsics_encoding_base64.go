package minigo

func (e *Engine) installEncodingBase64(h *hostHelpers) {
	e.Bind("encoding/base64", encodingBase64GenEntries())
}
