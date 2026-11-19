package strconv

import (
	"github.com/podhmo/minigo/internal/stdlib"
)

func Install(bind stdlib.Bind) {
	bind("strconv", strconvGenEntries())
}
