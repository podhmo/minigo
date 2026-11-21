package inner

// P is a nil pointer in a source-indexed package; comparing it to nil
// defers its member answer to run time.
var P *int
