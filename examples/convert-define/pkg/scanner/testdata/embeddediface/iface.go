package embeddediface

type Reader interface {
	Read(p []byte) (n int, err error)
}

type Writer interface {
	Write(p []byte) (n int, err error)
}

// ReadWriter embeds Reader and Writer and adds a new method.
type ReadWriter interface {
	Reader
	Writer
	Close() error
}
