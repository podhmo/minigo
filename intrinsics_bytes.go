package minigo

func (e *Engine) installBytes(h *hostHelpers) {
	e.bindBound("bytes", bytesGenEntries())
}
