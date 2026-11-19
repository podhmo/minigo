package bytes

import (
	"github.com/podhmo/minigo/internal/stdlib"
)

// Install binds bytes: the table is generated; rune-predicate callbacks
// (Skip list in genbind) stay unbound for now.
func Install(bind stdlib.Bind) {
	bind("bytes", bytesGenEntries())
}
