// Package tmpllex is a verbatim copy of text/template/parse's lexer
// (lex.go, package clause aside — see TestLexMatchesGOROOT) with an
// exported surface. minigo runs an interpreted text/template/parse but
// lexes on the host: the lexer's per-rune state machine dominated
// template parsing, while the parser and its trees stay script values
// with Go's semantics.
package tmpllex

import (
	_ "embed"
	"strings"
)

//go:embed lex.go
var source string

// SameSource reports whether src — a text/template/parse lex.go — is
// the file this package copies, so a host lexer can stand in for it.
// A toolchain whose lexer drifted keeps the interpreted one.
func SameSource(src []byte) bool {
	return strings.Replace(string(src), "\npackage parse\n", "\npackage tmpllex\n", 1) == source
}

// Pos mirrors text/template/parse.Pos (declared in node.go there).
type Pos int

// Item is one lexed token. Typ and Pos carry the values of the source
// package's itemType and Pos.
type Item struct {
	Typ  int
	Pos  int
	Val  string
	Line int
}

// Lexer scans one template text.
type Lexer struct{ l *lexer }

// Options are the parser-set lexer options (lexOptions).
type Options struct {
	EmitComment, BreakOK, ContinueOK bool
}

// New returns a lexer as text/template/parse's lex does.
func New(name, input, left, right string) *Lexer {
	return &Lexer{l: lex(name, input, left, right)}
}

// SetOptions sets the options the parser assigns before the first item.
func (x *Lexer) SetOptions(o Options) {
	x.l.options = lexOptions{emitComment: o.EmitComment, breakOK: o.BreakOK, continueOK: o.ContinueOK}
}

// NextItem returns the next item, as nextItem does.
func (x *Lexer) NextItem() Item {
	it := x.l.nextItem()
	return Item{Typ: int(it.typ), Pos: int(it.pos), Val: it.val, Line: it.line}
}

// ItemEOF and ItemError are the terminal item types.
const (
	ItemEOF   = int(itemEOF)
	ItemError = int(itemError)
)
