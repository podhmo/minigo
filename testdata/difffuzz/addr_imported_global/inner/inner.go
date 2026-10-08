package inner

type Token struct{}

var Allow Token

type Full struct{ X int }

var F Full

var N int

func Bump() { F.X++; N++ }

func IsAllow(p *Token) bool { return p == &Allow }
