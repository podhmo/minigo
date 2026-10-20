// Package inithookset installs inithook.End from its init(), like
// go/scanner installs scannerhooks.StringEnd.
package inithookset

import "github.com/podhmo/minigo/testdata/inithook"

type S struct{ n int }

func init() {
	inithook.End = func(x any) int { return x.(*S).n }
}

func (s *S) Init(n int) { s.n = n }
