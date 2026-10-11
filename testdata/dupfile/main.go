package dupfile

// Same is declared twice — illegal Go, but inspect.FileOf must still
// enumerate both decls rather than failing the load.
func Same() string { return "first" }

func Same() string { return "second" }

func Other() {}
