package resolve

import "errors"

// ErrNoBuildableGoFiles marks directories whose constraint-filtered file
// list is empty. Callers need errors.Is because the message carries file
// and constraint detail.
var ErrNoBuildableGoFiles = errors.New("no buildable Go source files")
