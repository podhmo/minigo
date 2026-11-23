// Package inferdot is inferpkg's dot-imported call target.
package inferdot

import "github.com/podhmo/minigo/testdata/infercallee"

// NewDot returns an infercallee.T — `var x = NewDot()` through a dot
// import infers T.
func NewDot() infercallee.T { return infercallee.New() }
