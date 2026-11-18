package minigo

func (e *Engine) installBytes(h *hostHelpers) {
	e.Bind("bytes", bytesGenEntries())
}
