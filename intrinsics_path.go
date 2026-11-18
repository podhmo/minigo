package minigo

func (e *Engine) installPath(h *hostHelpers) {
	e.Bind("path", pathGenEntries())
}
