package sha256

import (
	"github.com/podhmo/minigo/internal/stdlib"
)

func Install(bind stdlib.Bind) {
	bind("crypto/sha256", cryptoSha256GenEntries())
}
