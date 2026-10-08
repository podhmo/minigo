package token

type Token int

const (
	A Token = iota
	B
)

type TokenInfo struct {
	Token Token
	Line  int
}
