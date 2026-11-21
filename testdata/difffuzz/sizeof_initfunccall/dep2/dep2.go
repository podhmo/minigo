package dep2

// F is a type here — a use site importing dep2 as `dep` judges
// `dep.F(v)` a conversion, but the declaring file's dep is dep1.
type F struct{ A, B int64 }
