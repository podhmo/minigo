package inner

type T struct{ X int }

func P() *T { return &T{X: 1} }
