package inner

type I interface{ M() int }

type Impl struct{}

func (Impl) M() int { return 7 }

var Items = []I{Impl{}}
