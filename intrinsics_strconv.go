package minigo

func (e *Engine) installStrconv(h *hostHelpers) {
	e.bindBound("strconv", strconvGenEntries())
}
