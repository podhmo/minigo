// Package user imports packages whose package clause differs from the
// import path's last element, without aliasing them.
package user

import (
	"github.com/podhmo/minigo/testdata/pkgclause/go-bar"
	"github.com/podhmo/minigo/testdata/pkgclause/yaml.v3"
)

// Config refers to an imported type through the declared name.
type Config struct {
	Doc yaml.Node
}

// Use selects through both declared names.
func Use() string { return yaml.Marshal("x") + bar.Name }
