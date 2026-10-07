package main

import (
	"fmt"

	"github.com/podhmo/minigo/testdata/difffuzz/param_shadows_pkg_field/token"
)

type P struct {
	tokens  []token.TokenInfo
	current int
}

func (p *P) next(token token.Token) bool {
	return p.current < len(p.tokens) && p.tokens[p.current].Token == token
}

func first(token token.TokenInfo) (tok token.Token) {
	tok = token.Token
	return
}

func kind(token token.TokenInfo) token.Token { return token.Token }

func main() {
	fmt.Println(kind(token.TokenInfo{Token: token.B}))
	eq := func(token token.Token, want token.Token) bool { return token == want }
	fmt.Println(eq(token.A, token.B), first(token.TokenInfo{Token: token.B}))
	p := &P{tokens: []token.TokenInfo{{Token: token.B}}}
	fmt.Println(p.next(token.A), p.next(token.B))
}
