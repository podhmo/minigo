package minigo

func (e *Engine) installHtml(h *hostHelpers) {
	e.Bind("html", htmlGenEntries())
}
