// Package html is the lazy installer for the bound "html" package (P2-12b):
// the generated table lives here and registers through Install when the
// engine first needs the path.
package html

import "github.com/podhmo/minigo/internal/stdlib"

// Install registers the "html" bound table through the engine's lazy binder.
func Install(bind stdlib.Bind) {
	bind("html", htmlGenEntries())
}
