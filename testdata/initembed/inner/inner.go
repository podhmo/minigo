package inner

// Conf is embedded by the caller; the package's init fails first.
type Conf struct{ A string }

var bad = func() int { panic("inner init went wrong") }()
