package inithelper

import "github.com/podhmo/minigo/testdata/inittable"

func Get() int { return inittable.Lookup() }
