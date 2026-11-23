// Package embedpkg exercises promoted-method Detail spelling: a method
// whose signature mentions selector-qualified types.
package embedpkg

import . "strings"

type Emb struct {
	N int
}

// Sig's signature spells `strings.Builder` — the import only exists in
// this file's scope.
func (e Emb) Sig(b *Builder) string { return TrimSpace(b.String()) }

func (e Emb) Plain(x int) int { return x }
