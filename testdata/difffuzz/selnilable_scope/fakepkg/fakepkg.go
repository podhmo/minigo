package fakepkg

// Nilable here is NOT nilable: a caller that aliases this package as
// "realpkg" must not flip the verdict for foo.T.
type Nilable struct{}
