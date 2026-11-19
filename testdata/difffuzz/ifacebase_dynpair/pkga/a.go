package pkga

type I interface{ m() }

type Impl struct{}

func (Impl) m() {}

// Assert answers the assert question package pkga's own
// interface{ m() } asks: it is pkga's unexported m, not just any m.
func Assert(x any) bool {
	_, ok := x.(interface{ m() })
	return ok
}
