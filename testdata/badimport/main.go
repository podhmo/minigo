// Package main pins lazy import failure reporting: an import that
// cannot be loaded binds as a lazy ImportRef, and inspect.Methods on
// it must surface the load error — not an argument-shape error.
package main

import (
	"github.com/podhmo/minigo/inspect"
)

// LoadErr traps with the import's load error.
func LoadErr() string {
	inspect.Methods(badpkg)
	return "unreachable"
}
