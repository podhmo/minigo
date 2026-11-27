package main

// needValue validates a space-separated flag value position.
func needValue(name string, i, argc int) error {
	if i >= argc {
		return usageErrorf("%s requires a value", name)
	}
	return nil
}
