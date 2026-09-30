package impls

// MyReader implements ifaces.SimpleReader.
type MyReader struct{}

func (r *MyReader) Read(p []byte) (n int, err error) {
	return 0, nil
}

// MyEmbeddedReader implements ifaces.EmbeddedReader.
type MyEmbeddedReader struct{}

func (r *MyEmbeddedReader) Read(p []byte) (n int, err error) {
	return 0, nil
}

func (r *MyEmbeddedReader) Close() error {
	return nil
}

// EmbeddedStruct provides a method for AnotherInterface.
type EmbeddedStruct struct{}

func (s *EmbeddedStruct) AnotherMethod() string {
	return "embedded"
}

// StructWithEmbedded implements AnotherInterface via embedding.
type StructWithEmbedded struct {
	EmbeddedStruct
}

// NonImplementer has no methods.
type NonImplementer struct{}

// PartialImplementer only implements part of EmbeddedReader.
type PartialImplementer struct{}

func (r *PartialImplementer) Read(p []byte) (n int, err error) {
	return 0, nil
}

// StructWithEmbeddedConcrete implements AnotherInterface via embedding a concrete type.
type StructWithEmbeddedConcrete struct {
	EmbeddedStruct
}
