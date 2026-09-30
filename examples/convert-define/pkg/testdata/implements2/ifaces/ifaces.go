package ifaces

// SimpleReader is a simple interface in its own package.
type SimpleReader interface {
	Read(p []byte) (n int, err error)
}

// EmbeddedReader embeds SimpleReader.
type EmbeddedReader interface {
	SimpleReader
	Close() error
}

// AnotherInterface is for testing embedded struct implementations.
type AnotherInterface interface {
	AnotherMethod() string
}
