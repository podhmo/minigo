package minigo

func (e *Engine) installCryptoSha256(h *hostHelpers) {
	e.bindBound("crypto/sha256", cryptoSha256GenEntries())
}
