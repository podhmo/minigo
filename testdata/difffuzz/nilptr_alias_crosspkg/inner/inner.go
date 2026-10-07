package inner

type Tree struct{ N int }

func Get(ok bool) (*Tree, error) {
	if ok {
		return &Tree{N: 1}, nil
	}
	return nil, nil
}
