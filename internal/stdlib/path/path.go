package path

import (
	"github.com/podhmo/minigo/internal/stdlib"
)

func Install(bind stdlib.Bind) {
	bind("path", pathGenEntries())
}
