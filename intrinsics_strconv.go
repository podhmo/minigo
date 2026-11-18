package minigo

func (e *Engine) installStrconv(h *hostHelpers) {
	e.Bind("strconv", strconvGenEntries())
}
