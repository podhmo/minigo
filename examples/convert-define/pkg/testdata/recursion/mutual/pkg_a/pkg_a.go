package pkg_a

import "example.com/recursion/mutual/pkg_b"

type A struct {
	B *pkg_b.B
}
