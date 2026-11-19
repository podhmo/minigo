package minigo

func (e *Engine) installPath(h *hostHelpers) {
	e.bindBound("path", pathGenEntries())
}
