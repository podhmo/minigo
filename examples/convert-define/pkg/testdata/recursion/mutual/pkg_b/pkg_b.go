package pkg_b

import "example.com/recursion/mutual/pkg_a"

type B struct {
	A *pkg_a.A
}
