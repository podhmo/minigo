package minigo

func (e *Engine) installCryptoSha256(h *hostHelpers) {
	e.Bind("crypto/sha256", cryptoSha256GenEntries())
}
