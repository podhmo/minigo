package minigo

func (e *Engine) installHtml(h *hostHelpers) {
	e.bindBound("html", htmlGenEntries())
}
